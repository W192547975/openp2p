package openp2p

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func Run() {
	network := run(filepath.Dir(os.Args[0]), func() bool {
		if len(os.Args) > 1 {
			switch os.Args[1] {
			case "version", "-v", "--version":
				fmt.Println(OpenP2PVersion)
				return true
			case "install":
				install()
				return true
			case "uninstall":
				uninstall()
				return true
			}
		} else {
			installByFilename()
		}
		parseParams("")
		return false
	}, func() bool {
		if gConf.daemonMode {
			d := daemon{}
			d.run()
			return true
		}
		
		setFirewall()
		err := setRLimit()
		if err != nil {
			gLog.Println(LvINFO, "setRLimit error:", err)
		}
		return false
	})
	if network != nil {
		<-chan int(nil) // forever
	}
}

var network *P2PNetwork

// for Android app
// gomobile not support uint64 exported to java
func RunAsModule(baseDir string, token string, bw int, logLevel int) *P2PNetwork {
	return run(baseDir, func() bool {
		parseParams("")
		
		n, err := strconv.ParseUint(token, 10, 64)
		if err == nil {
			gConf.setToken(n)
		}
		gLog.setLevel(LogLevel(logLevel))
		gConf.setShareBandwidth(bw)
		return false
	}, nil)
}

func run(baseDir string, beforeStart, afterStart func() bool) *P2PNetwork {
	rand.Seed(time.Now().UnixNano())
	os.Chdir(baseDir) // for system service
	gLog = NewLogger(baseDir, ProductName, LvDEBUG, 1024*1024, LogFile|LogConsole)
	if beforeStart != nil && beforeStart() {
		return nil
	}
	gLog.Println(LvINFO, "openp2p start. version: ", OpenP2PVersion)
	gLog.Println(LvINFO, "Contact: QQ group 16947733, Email openp2p.cn@gmail.com")
	gLog.Println(LvINFO, &gConf)
	if afterStart != nil && afterStart() {
		return nil
	}
	network = P2PNetworkInstance(&gConf.Network)
	if ok := network.Connect(30000); !ok {
		gLog.Println(LvERROR, "P2PNetwork login error")
		return nil
	}
	// gLog.Println(LvINFO, "waiting for connection...")
	return network
}

func GetToken(baseDir string) string {
	os.Chdir(baseDir)
	gConf.load()
	return fmt.Sprintf("%d", gConf.Network.Token)
}
