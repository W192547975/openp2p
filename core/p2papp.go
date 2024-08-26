package openp2p

import (
	"bytes"
	"encoding/binary"
	"sync"
	"time"
)

type p2pApp struct {
	tunnel    *P2PTunnel
	rtid      uint64 // relay tunnelID
	relayNode string
	relayMode string
	hbTime    time.Time
	hbMtx     sync.Mutex
	running   bool
	id        uint64
	wg        sync.WaitGroup
}

func (app *p2pApp) isActive() bool {
	if app.tunnel == nil {
		return false
	}
	if app.rtid == 0 { // direct mode app heartbeat equals to tunnel heartbeat
		return app.tunnel.isActive()
	}
	// relay mode calc app heartbeat
	app.hbMtx.Lock()
	defer app.hbMtx.Unlock()
	return time.Now().Before(app.hbTime.Add(TunnelIdleTimeout))
}

func (app *p2pApp) updateHeartbeat() {
	app.hbMtx.Lock()
	defer app.hbMtx.Unlock()
	app.hbTime = time.Now()
}

func (app *p2pApp) close() {
	app.running = false
	if app.tunnel != nil {
		app.tunnel.closeOverlayConns(app.id)
	}
	app.wg.Wait()
}

// TODO: many relay app on the same P2PTunnel will send a lot of relay heartbeat
func (app *p2pApp) relayHeartbeatLoop() {
	app.wg.Add(1)
	defer app.wg.Done()
	gLog.Printf(LvDEBUG, "relayHeartbeat to rtid:%d start", app.rtid)
	defer gLog.Printf(LvDEBUG, "relayHeartbeat to rtid%d end", app.rtid)
	var msgWithHead bytes.Buffer
	binary.Write(&msgWithHead, binary.LittleEndian, app.rtid)
	req := RelayHeartbeat{RelayTunnelID: app.tunnel.id, AppID: app.id}
	msg, _ := newMessage(MsgP2P, MsgRelayHeartbeat, &req)
	msgWithHead.Write(msg)
	for app.tunnel.isRuning() && app.running {
		err := app.tunnel.conn.WriteBytes(MsgP2P, MsgRelayData, msgWithHead.Bytes())
		if err != nil {
			gLog.Printf(LvERROR, "%d app write relay tunnel heartbeat error %s", app.rtid, err)
			return
		}
		gLog.Printf(LvDEBUG, "%d app write relay tunnel heartbeat ok", app.rtid)
		time.Sleep(TunnelHeartbeatTime)
	}
}
