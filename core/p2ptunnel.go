package openp2p

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"
)

type P2PTunnel struct {
	pn            *P2PNetwork
	conn          underlay
	hbTime        time.Time
	hbMtx         sync.Mutex
	config        AppConfig
	la            *net.UDPAddr // local hole address
	ra            *net.UDPAddr // remote hole address
	appKeys       map[uint64][AESKeySize]byte
	id            uint64
	running       bool
	runMtx        sync.Mutex
	tunnelServer  bool // different from underlayServer
	coneLocalPort int
	coneNatPort   int
	linkModeWeb   string // use config.linkmode
	punchTs       uint64
}

func (t *P2PTunnel) initPort() {
	t.running = true
	localPort := int(rand.Uint32()%15000 + 50000) // if the process has bug, will add many upnp port. use specify p2p port by param
	if t.config.linkMode == LinkModeTCP6 || t.config.linkMode == LinkModeTCP4 {
		t.coneLocalPort = t.pn.config.TCPPort
		t.coneNatPort = t.pn.config.TCPPort // symmetric doesn't need coneNatPort
	}
	if t.config.linkMode == LinkModeUDPPunch {
		// prepare one random cone hole manually
		_, natPort, _ := natTest(t.pn.config.ServerHost, t.pn.config.UDPPort1, localPort)
		t.coneLocalPort = localPort
		t.coneNatPort = natPort
	}
	if t.config.linkMode == LinkModeTCPPunch {
		// prepare one random cone hole by system automatically
		_, natPort, localPort2 := natTCP(t.pn.config.ServerHost, IfconfigPort1)
		t.coneLocalPort = localPort2
		t.coneNatPort = natPort
	}
	t.la = &net.UDPAddr{IP: net.ParseIP(t.pn.config.localIP), Port: t.coneLocalPort}
	gLog.Printf(LvDEBUG, "prepare punching port %d:%d", t.coneLocalPort, t.coneNatPort)
}

func (t *P2PTunnel) connect() error {
	gLog.Printf(LvDEBUG, "start p2pTunnel to %s ", t.config.PeerNode)
	t.tunnelServer = false
	appKey := uint64(0)
	req := PushConnectReq{
		Token:            t.config.peerToken,
		From:             t.pn.config.Node,
		FromIP:           t.pn.config.publicIP,
		ConeNatPort:      t.coneNatPort,
		NatType:          t.pn.config.natType,
		HasIPv4:          t.pn.config.hasIPv4,
		IPv6:             gConf.IPv6(),
		HasUPNPorNATPMP:  t.pn.config.hasUPNPorNATPMP,
		ID:               t.id,
		AppKey:           appKey,
		Version:          OpenP2PVersion,
		LinkMode:         t.config.linkMode,
		IsUnderlayServer: t.config.isUnderlayServer ^ 1, // peer
	}
	if req.Token == 0 { // no relay token
		req.Token = t.pn.config.Token
	}
	t.pn.push(t.config.PeerNode, MsgPushConnectReq, req)
	head, body := t.pn.read(t.config.PeerNode, MsgPush, MsgPushConnectRsp, UnderlayConnectTimeout*3)
	if head == nil {
		return errors.New("connect error")
	}
	rsp := PushConnectRsp{}
	if err := json.Unmarshal(body, &rsp); err != nil {
		gLog.Printf(LvERROR, "wrong %T:%s", rsp, err)
		return err
	}
	// gLog.Println(LevelINFO, rsp)
	if rsp.Error != 0 {
		return errors.New(rsp.Detail)
	}
	t.config.peerNatType = rsp.NatType
	t.config.hasIPv4 = rsp.HasIPv4
	t.config.peerIPv6 = rsp.IPv6
	t.config.hasUPNPorNATPMP = rsp.HasUPNPorNATPMP
	t.config.peerVersion = rsp.Version
	t.config.peerConeNatPort = rsp.ConeNatPort
	t.config.peerIP = rsp.FromIP
	t.punchTs = rsp.PunchTs
	err := t.start()
	if err != nil {
		gLog.Println(LvERROR, "handshake error:", err)
		err = ErrorHandshake
	}
	return err
}

func (t *P2PTunnel) isRuning() bool {
	t.runMtx.Lock()
	defer t.runMtx.Unlock()
	return t.running
}

func (t *P2PTunnel) setRun(running bool) {
	t.runMtx.Lock()
	defer t.runMtx.Unlock()
	t.running = running
}

func (t *P2PTunnel) isActive() bool {
	if !t.isRuning() || t.conn == nil {
		return false
	}
	t.hbMtx.Lock()
	defer t.hbMtx.Unlock()
	return time.Now().Before(t.hbTime.Add(TunnelHeartbeatTime * 2))
}

func (t *P2PTunnel) checkActive() bool {
	if !t.isActive() {
		return false
	}
	hbt := time.Now()
	t.conn.WriteBytes(MsgP2P, MsgTunnelHeartbeat, nil)
	isActive := false
	// wait at most 5s
	for i := 0; i < 50 && !isActive; i++ {
		t.hbMtx.Lock()
		if t.hbTime.After(hbt) {
			isActive = true
		}
		t.hbMtx.Unlock()
		time.Sleep(time.Millisecond * 100)
	}
	gLog.Printf(LvINFO, "checkActive %t. hbtime=%d", isActive, t.hbTime)
	return isActive
}

// call when user delete tunnel
func (t *P2PTunnel) close() {
	t.setRun(false)
	t.pn.allTunnels.Delete(t.id)
}

func (t *P2PTunnel) start() error {
	if t.config.linkMode == LinkModeUDPPunch {
		if err := t.handshake(); err != nil {
			return err
		}
	}
	err := t.connectUnderlay()
	if err != nil {
		gLog.Println(LvERROR, err)
		return err
	}
	return nil
}

func (t *P2PTunnel) handshake() error {
	if t.config.peerConeNatPort > 0 { // only peer is cone should prepare t.ra
		var err error
		t.ra, err = net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", t.config.peerIP, t.config.peerConeNatPort))
		if err != nil {
			return err
		}
	}
	if compareVersion(t.config.peerVersion, SyncServerTimeVersion) == LESS {
		gLog.Printf(LvDEBUG, "peer version %s less than %s", t.config.peerVersion, SyncServerTimeVersion)
	} else {
		ts := time.Duration(int64(t.punchTs) + t.pn.dt + t.pn.ddtma*int64(time.Since(t.pn.hbTime)+PunchTsDelay)/int64(NetworkHeartbeatTime) - time.Now().UnixNano())
		gLog.Printf(LvDEBUG, "sleep %d ms", ts/time.Millisecond)
		time.Sleep(ts)
	}
	gLog.Println(LvDEBUG, "handshake to ", t.config.PeerNode)
	var err error
	if t.pn.config.natType == NATCone && t.config.peerNatType == NATCone {
		err = handshakeC2C(t)
	} else if t.config.peerNatType == NATSymmetric && t.pn.config.natType == NATSymmetric {
		err = ErrorS2S
		t.close()
	} else if t.config.peerNatType == NATSymmetric && t.pn.config.natType == NATCone {
		err = handshakeC2S(t)
	} else if t.config.peerNatType == NATCone && t.pn.config.natType == NATSymmetric {
		err = handshakeS2C(t)
	} else {
		return errors.New("unknown error")
	}
	if err != nil {
		gLog.Println(LvERROR, "punch handshake error:", err)
		return err
	}
	gLog.Printf(LvDEBUG, "handshake to %s ok", t.config.PeerNode)
	return nil
}

func (t *P2PTunnel) connectUnderlay() (err error) {
	switch t.config.linkMode {
	case LinkModeTCP6:
		t.conn, err = t.connectUnderlayTCP6()
	case LinkModeTCP4:
		t.conn, err = t.connectUnderlayTCP()
	case LinkModeTCPPunch:
		t.conn, err = t.connectUnderlayTCP()
	case LinkModeUDPPunch:
		t.conn, err = t.connectUnderlayQuic()

	}
	if err != nil {
		return err
	}
	if t.conn == nil {
		return errors.New("connect underlay error")
	}
	t.setRun(true)
	go t.readLoop()
	go t.heartbeatLoop()
	return nil
}

func (t *P2PTunnel) connectUnderlayQuic() (c underlay, err error) {
	gLog.Println(LvINFO, "connectUnderlayQuic start")
	defer gLog.Println(LvINFO, "connectUnderlayQuic end")
	var ul *underlayQUIC
	if t.config.isUnderlayServer == 1 {
		time.Sleep(time.Millisecond * 10) // punching udp port will need some times in some env
		ul, err = listenQuic(t.la.String(), TunnelIdleTimeout)
		if err != nil {
			gLog.Println(LvINFO, "listen quic error:", err, ", retry...")
		}
		t.pn.push(t.config.PeerNode, MsgPushUnderlayConnect, nil)
		err = ul.Accept()
		if err != nil {
			ul.CloseListener()
			return nil, fmt.Errorf("accept quic error:%s", err)
		}
		_, buff, err := ul.ReadBuffer()
		if err != nil {
			ul.listener.Close()
			return nil, fmt.Errorf("read start msg error:%s", err)
		}
		if buff != nil {
			gLog.Println(LvDEBUG, string(buff))
		}
		ul.WriteBytes(MsgP2P, MsgTunnelHandshakeAck, []byte("OpenP2P,hello2"))
		gLog.Println(LvDEBUG, "quic connection ok")
		return ul, nil
	}

	//else
	conn, e := net.ListenUDP("udp", t.la)
	if e != nil {
		time.Sleep(time.Millisecond * 10)
		conn, e = net.ListenUDP("udp", t.la)
		if e != nil {
			return nil, fmt.Errorf("quic listen error:%s", e)
		}
	}
	t.pn.read(t.config.PeerNode, MsgPush, MsgPushUnderlayConnect, ReadMsgTimeout)
	gLog.Println(LvDEBUG, "quic dial to ", t.ra.String())
	ul, e = dialQuic(conn, t.ra, TunnelIdleTimeout)
	if e != nil {
		return nil, fmt.Errorf("quic dial to %s error:%s", t.ra.String(), e)
	}
	handshakeBegin := time.Now()
	ul.WriteBytes(MsgP2P, MsgTunnelHandshake, []byte("OpenP2P,hello"))
	_, buff, err := ul.ReadBuffer()
	if e != nil {
		ul.listener.Close()
		return nil, fmt.Errorf("read MsgTunnelHandshake error:%s", err)
	}
	if buff != nil {
		gLog.Println(LvDEBUG, string(buff))
	}

	gLog.Println(LvINFO, "rtt=", time.Since(handshakeBegin))
	gLog.Println(LvDEBUG, "quic connection ok")
	t.linkModeWeb = LinkModeUDPPunch
	return ul, nil
}

// websocket
func (t *P2PTunnel) connectUnderlayTCP() (c underlay, err error) {
	gLog.Println(LvDEBUG, "connectUnderlayTCP start")
	defer gLog.Println(LvDEBUG, "connectUnderlayTCP end")
	var ul *underlayTCP
	if t.config.isUnderlayServer == 1 {
		ul, err = listenTCP(t.config.peerIP, t.config.peerConeNatPort, t.coneLocalPort, t.config.linkMode, t)
		if err != nil {
			return nil, fmt.Errorf("listen TCP error:%s", err)
		}
		gLog.Println(LvINFO, "TCP connection ok")
		return ul, nil
	}

	// client side
	if t.config.linkMode == LinkModeTCP4 {
		t.pn.read(t.config.PeerNode, MsgPush, MsgPushUnderlayConnect, ReadMsgTimeout)
	} else { //tcp punch should sleep for punch the same time
		if compareVersion(t.config.peerVersion, SyncServerTimeVersion) == LESS {
			gLog.Printf(LvDEBUG, "peer version %s less than %s", t.config.peerVersion, SyncServerTimeVersion)
		} else {
			ts := time.Duration(int64(t.punchTs) + t.pn.dt + t.pn.ddtma*int64(time.Since(t.pn.hbTime)+PunchTsDelay)/int64(NetworkHeartbeatTime) - time.Now().UnixNano())
			gLog.Printf(LvDEBUG, "sleep %d ms", ts/time.Millisecond)
			time.Sleep(ts)
		}
	}
	ul, err = dialTCP(t.config.peerIP, t.config.peerConeNatPort, t.coneLocalPort, t.config.linkMode)
	if err != nil {
		return nil, fmt.Errorf("TCP dial to %s:%d error:%s", t.config.peerIP, t.config.peerConeNatPort, err)
	}
	handshakeBegin := time.Now()
	tidBuff := new(bytes.Buffer)
	binary.Write(tidBuff, binary.LittleEndian, t.id)
	ul.WriteBytes(MsgP2P, MsgTunnelHandshake, tidBuff.Bytes()) //  tunnelID
	_, buff, err := ul.ReadBuffer()
	if err != nil {
		return nil, fmt.Errorf("read MsgTunnelHandshake error:%s", err)
	}
	if buff != nil {
		gLog.Println(LvDEBUG, "hello ", string(buff))
	}

	gLog.Println(LvINFO, "rtt=", time.Since(handshakeBegin))
	gLog.Println(LvINFO, "TCP connection ok")
	t.linkModeWeb = LinkModeIPv4
	return ul, nil
}

func (t *P2PTunnel) connectUnderlayTCP6() (c underlay, err error) {
	gLog.Println(LvINFO, "connectUnderlayTCP6 start")
	defer gLog.Println(LvINFO, "connectUnderlayTCP6 end")
	var ul *underlayTCP6
	if t.config.isUnderlayServer == 1 {
		t.pn.push(t.config.PeerNode, MsgPushUnderlayConnect, nil)
		ul, err = listenTCP6(t.coneNatPort, UnderlayConnectTimeout)
		if err != nil {
			return nil, fmt.Errorf("listen TCP6 error:%s", err)
		}
		_, buff, err := ul.ReadBuffer()
		if err != nil {
			ul.listener.Close()
			return nil, fmt.Errorf("read start msg error:%s", err)
		}
		if buff != nil {
			gLog.Println(LvDEBUG, string(buff))
		}
		ul.WriteBytes(MsgP2P, MsgTunnelHandshakeAck, []byte("OpenP2P,hello2"))
		gLog.Println(LvDEBUG, "TCP6 connection ok")
		return ul, nil
	}

	//else
	t.pn.read(t.config.PeerNode, MsgPush, MsgPushUnderlayConnect, ReadMsgTimeout)
	gLog.Println(LvDEBUG, "TCP6 dial to ", t.config.peerIPv6)
	ul, err = dialTCP6(t.config.peerIPv6, t.config.peerConeNatPort)
	if err != nil {
		return nil, fmt.Errorf("TCP6 dial to %s:%d error:%s", t.config.peerIPv6, t.config.peerConeNatPort, err)
	}
	handshakeBegin := time.Now()
	ul.WriteBytes(MsgP2P, MsgTunnelHandshake, []byte("OpenP2P,hello"))
	_, buff, err := ul.ReadBuffer()
	if err != nil {
		ul.listener.Close()
		return nil, fmt.Errorf("read MsgTunnelHandshake error:%s", err)
	}
	if buff != nil {
		gLog.Println(LvDEBUG, string(buff))
	}

	gLog.Println(LvINFO, "rtt=", time.Since(handshakeBegin))
	gLog.Println(LvDEBUG, "TCP6 connection ok")
	t.linkModeWeb = LinkModeIPv6
	return ul, nil
}

func (t *P2PTunnel) readLoop() {
	decryptData := make([]byte, ReadBuffLen+PaddingSize) // 16 bytes for padding
	gLog.Printf(LvDEBUG, "%d tunnel readloop start", t.id)
	for t.isRuning() {
		t.conn.SetReadDeadline(time.Now().Add(TunnelIdleTimeout))
		head, body, err := t.conn.ReadBuffer()
		if err != nil {
			if t.isRuning() {
				gLog.Printf(LvERROR, "%d tunnel read error:%s", t.id, err)
			}
			break
		}
		if head.MainType != MsgP2P {
			gLog.Printf(LvWARN, "%d head.MainType != MsgP2P", t.id)
			continue
		}
		switch head.SubType {
		case MsgTunnelHeartbeat:
			t.hbTime = time.Now()
			t.conn.WriteBytes(MsgP2P, MsgTunnelHeartbeatAck, nil)
			gLog.Printf(LvDEBUG, "%d read tunnel heartbeat", t.id)
		case MsgTunnelHeartbeatAck:
			t.hbMtx.Lock()
			t.hbTime = time.Now()
			t.hbMtx.Unlock()
			gLog.Printf(LvDEBUG, "%d read tunnel heartbeat ack", t.id)
		case MsgOverlayData:
			if len(body) < overlayHeaderSize {
				gLog.Printf(LvWARN, "%d len(body) < overlayHeaderSize", t.id)
				continue
			}
			overlayID := binary.LittleEndian.Uint64(body[:8])
			gLog.Printf(LvDEBUG, "%d tunnel read overlay data %d bodylen=%d: %x", t.id, overlayID, head.DataLen, body[8:])
			appKeyBytes, ok := t.appKeys[overlayID]
			if !ok {
				// debug level, when overlay connection closed, always has some packet not found tunnel
				gLog.Printf(LvDEBUG, "%d tunnel not found overlay connection %d", t.id, overlayID)
				continue
			}
			payload := body[overlayHeaderSize:]
			if appKeyBytes != [AESKeySize]byte{} {
				payload, _ = decryptBytes(appKeyBytes[:], decryptData, body[overlayHeaderSize:], int(head.DataLen)-overlayHeaderSize)
				gLog.Printf(LvDEBUG, "%d decrypted data: %x", overlayID, payload)
			}
		case MsgRelayData:
			if len(body) < 8 {
				continue
			}
			tunnelID := binary.LittleEndian.Uint64(body[:8])
			gLog.Printf(LvDEBUG, "relay data to %d, len=%d", tunnelID, head.DataLen-RelayHeaderSize)
			t.pn.relay(tunnelID, body[RelayHeaderSize:])
		case MsgRelayHeartbeat:
			req := RelayHeartbeat{}
			if err := json.Unmarshal(body, &req); err != nil {
				gLog.Printf(LvWARN, "wrong %T:%s", req, err)
				continue
			}
			gLog.Printf(LvDEBUG, "read MsgRelayHeartbeat from rtid:%d,appid:%d", req.RelayTunnelID, req.AppID)
			relayHead := new(bytes.Buffer)
			binary.Write(relayHead, binary.LittleEndian, req.RelayTunnelID)
			msg, _ := newMessage(MsgP2P, MsgRelayHeartbeatAck, &req)
			msgWithHead := append(relayHead.Bytes(), msg...)
			t.conn.WriteBytes(MsgP2P, MsgRelayData, msgWithHead)
		case MsgRelayHeartbeatAck:
			req := RelayHeartbeat{}
			err := json.Unmarshal(body, &req)
			if err != nil {
				gLog.Printf(LvERROR, "wrong RelayHeartbeat:%s", err)
				continue
			}
			gLog.Printf(LvDEBUG, "read MsgRelayHeartbeatAck to appid:%d", req.AppID)
			t.pn.updateAppHeartbeat(req.AppID)
		case MsgOverlayConnectReq:
			var req OverlayConnectReq
			if err := json.Unmarshal(body, &req); err != nil {
				gLog.Printf(LvWARN, "wrong %T:%s", req, err)
				continue
			}
			gLog.Printf(LvDEBUG, "%d get a overlay connection: %+v", t.id, req)

			// calc key bytes for encrypt
			if t.appKeys == nil {
				t.appKeys = make(map[uint64][AESKeySize]byte)
			}
			var encryptKey [AESKeySize]byte
			appKey := GetKey(req.AppID)
			if appKey != 0 {
				gLog.Println(LvDEBUG, req.ID, " has appKey ", appKey)
			}
			binary.LittleEndian.PutUint64(encryptKey[:8], appKey)
			binary.LittleEndian.PutUint64(encryptKey[8:], appKey)
			t.appKeys[req.ID] = encryptKey

		case MsgOverlayDisconnectReq:
			req := OverlayDisconnectReq{}
			if err := json.Unmarshal(body, &req); err != nil {
				gLog.Printf(LvWARN, "wrong %T:%s", req, err)
				continue
			}
			overlayID := req.ID
			gLog.Printf(LvDEBUG, "%d disconnect overlay connection %d", t.id, overlayID)
			delete(t.appKeys, overlayID)
		default:
		}
	}
	t.setRun(false)
	t.conn.Close()
	gLog.Printf(LvDEBUG, "%d tunnel readloop end", t.id)
}

func (t *P2PTunnel) heartbeatLoop() {
	t.hbMtx.Lock()
	t.hbTime = time.Now() // init
	t.hbMtx.Unlock()
	tc := time.NewTicker(TunnelHeartbeatTime)
	defer tc.Stop()
	gLog.Printf(LvDEBUG, "%d tunnel heartbeatLoop start", t.id)
	defer gLog.Printf(LvDEBUG, "%d tunnel heartbeatLoop end", t.id)
	for t.isRuning() {
		select {
		case <-tc.C:
			// tunnel send
			err := t.conn.WriteBytes(MsgP2P, MsgTunnelHeartbeat, nil)
			if err != nil {
				gLog.Printf(LvERROR, "%d write tunnel heartbeat error %s", t.id, err)
				t.setRun(false)
				return
			}
			gLog.Printf(LvDEBUG, "%d write tunnel heartbeat ok", t.id)
		}
	}
}

func (t *P2PTunnel) listen() error {
	// notify client to connect
	rsp := PushConnectRsp{
		Error:   0,
		Detail:  "connect ok",
		To:      t.config.PeerNode,
		From:    t.pn.config.Node,
		NatType: t.pn.config.natType,
		HasIPv4: t.pn.config.hasIPv4,
		// IPv6:            t.pn.config.IPv6,
		HasUPNPorNATPMP: t.pn.config.hasUPNPorNATPMP,
		FromIP:          t.pn.config.publicIP,
		ConeNatPort:     t.coneNatPort,
		ID:              t.id,
		PunchTs:         uint64(time.Now().UnixNano() + int64(PunchTsDelay) - t.pn.dt),
		Version:         OpenP2PVersion,
	}
	t.punchTs = rsp.PunchTs
	// only private node set ipv6
	if t.config.fromToken == t.pn.config.Token {
		rsp.IPv6 = gConf.IPv6()
	}

	t.pn.push(t.config.PeerNode, MsgPushConnectRsp, rsp)
	gLog.Printf(LvDEBUG, "p2ptunnel wait for connecting")
	t.tunnelServer = true
	return t.start()
}
