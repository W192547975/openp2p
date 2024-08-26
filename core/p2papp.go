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
	id        uint64
	runMtx    sync.Mutex
	stop      chan error
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
	app.runMtx.Lock()
	stop := app.stop
	app.runMtx.Unlock() // care deadlock
	if stop != nil {
		<-stop // ignore error
	}
}

// TODO: many relay app on the same P2PTunnel will send a lot of relay heartbeat
func (app *p2pApp) startRelayHeartbeat(stop chan error) {
	// app.runMtx.Lock()
	app.stop = stop
	// app.runMtx.Unlock()
	var msgWithHead bytes.Buffer
	binary.Write(&msgWithHead, binary.LittleEndian, app.rtid)
	req := RelayHeartbeat{RelayTunnelID: app.tunnel.id, AppID: app.id}
	msg, _ := newMessage(MsgP2P, MsgRelayHeartbeat, &req)
	msgWithHead.Write(msg)
	go func(msgData []byte) {
		gLog.Printf(LvDEBUG, "relayHeartbeat to rtid:%d start", app.rtid)
		defer gLog.Printf(LvDEBUG, "relayHeartbeat to rtid%d end", app.rtid)
		for app.tunnel.isRuning() {
			// app.runMtx.Lock()
			// var stop = app.stop
			// app.runMtx.Unlock()
			select {
			case app.stop <- nil:
				return
			default:
				err := app.tunnel.conn.WriteBytes(MsgP2P, MsgRelayData, msgData)
				if err != nil {
					gLog.Printf(LvERROR, "%d app write relay tunnel heartbeat error %s", app.rtid, err)
					app.runMtx.Lock()
					stop := app.stop
					if stop == nil {
						stop = make(chan error, 1)
						app.stop = stop
					}
					app.runMtx.Unlock() // care deadlock
					stop <- err
					return
				}
				gLog.Printf(LvDEBUG, "%d app write relay tunnel heartbeat ok", app.rtid)
				time.Sleep(TunnelHeartbeatTime)
			}
		}
	}(msgWithHead.Bytes())
}
