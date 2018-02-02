package dispatcherclient

import (
	"time"

	"net"

	"fmt"

	"sync/atomic"
	"unsafe"

	"github.com/pkg/errors"
	"github.com/xiaonanln/goworld/engine/config"
	"github.com/xiaonanln/goworld/engine/consts"
	"github.com/xiaonanln/goworld/engine/gwioutil"
	"github.com/xiaonanln/goworld/engine/gwlog"
	"github.com/xiaonanln/goworld/engine/gwutils"
	"github.com/xiaonanln/goworld/engine/netutil"
	"github.com/xiaonanln/goworld/engine/proto"
)

const (
	_LOOP_DELAY_ON_DISPATCHER_CLIENT_ERROR = time.Second
)

type DispatcherConnMgr struct {
	gid               uint16 // gateid or gameid
	dctype            DispatcherClientType
	dispid            uint16
	_dispatcherClient *DispatcherClient
	isReconnect       bool
	isRestoreGame     bool
	delegate          IDispatcherClientDelegate
}

var (
	errDispatcherNotConnected = errors.New("dispatcher not connected")
)

func NewDispatcherConnMgr(gid uint16, dctype DispatcherClientType, dispid uint16, isRestoreGame bool, delegate IDispatcherClientDelegate) *DispatcherConnMgr {
	return &DispatcherConnMgr{
		dctype:        dctype,
		dispid:        dispid,
		isRestoreGame: isRestoreGame,
		delegate:      delegate,
	}
}

func (dcm *DispatcherConnMgr) getDispatcherClient() *DispatcherClient { // atomic
	addr := (*uintptr)(unsafe.Pointer(&dcm._dispatcherClient))
	return (*DispatcherClient)(unsafe.Pointer(atomic.LoadUintptr(addr)))
}

func (dcm *DispatcherConnMgr) setDispatcherClient(dispatcherClient *DispatcherClient) { // atomic
	addr := (*uintptr)(unsafe.Pointer(&dcm._dispatcherClient))
	atomic.StoreUintptr(addr, uintptr(unsafe.Pointer(dispatcherClient)))
}

func (dcm *DispatcherConnMgr) String() string {
	return fmt.Sprintf("DispatcherConnMgr<%d>", dcm.dispid)
}

func (dcm *DispatcherConnMgr) assureConnected() *DispatcherClient {
	//gwlog.Debugf("assureConnected: _dispatcherClient", _dispatcherClient)
	var err error
	dc := dcm.getDispatcherClient()
	for dc == nil || dc.IsClosed() {
		dc, err = dcm.connectDispatchClient()
		if err != nil {
			gwlog.Errorf("Connect to dispatcher failed: %s", err.Error())
			time.Sleep(_LOOP_DELAY_ON_DISPATCHER_CLIENT_ERROR)
			continue
		}
		dcm.setDispatcherClient(dc)
		if dcm.dctype == GameDispatcherClientType {
			dc.SendSetGameID(dcm.gid, dcm.isReconnect, dcm.isRestoreGame)
		} else {
			dc.SendSetGateID(dcm.gid)
		}
		dcm.isReconnect = true

		gwlog.Infof("dispatcher_client: connected to dispatcher: %s", dc)
	}
	return dc
}

func (dcm *DispatcherConnMgr) connectDispatchClient() (*DispatcherClient, error) {
	dispatcherConfig := config.GetDispatcher(dcm.dispid)
	conn, err := netutil.ConnectTCP(dispatcherConfig.Ip, dispatcherConfig.Port)
	if err != nil {
		return nil, err
	}
	tcpConn := conn.(*net.TCPConn)
	tcpConn.SetReadBuffer(consts.DISPATCHER_CLIENT_READ_BUFFER_SIZE)
	tcpConn.SetWriteBuffer(consts.DISPATCHER_CLIENT_WRITE_BUFFER_SIZE)
	dc := newDispatcherClient(dcm.dctype, conn, dcm.isReconnect, dcm.isRestoreGame)
	return dc, nil
}

// IDispatcherClientDelegate defines functions that should be implemented by dispatcher clients
type IDispatcherClientDelegate interface {
	HandleDispatcherClientPacket(msgtype proto.MsgType, packet *netutil.Packet)
	HandleDispatcherClientDisconnect()
}

// Initialize the dispatcher client, only called by engine
func (dcm *DispatcherConnMgr) Connect() {
	dcm.assureConnected()
	go gwutils.RepeatUntilPanicless(dcm.serveDispatcherClient) // start the recv routine
}

// GetDispatcherClientForSend returns the current dispatcher client for sending messages
func (dcm *DispatcherConnMgr) GetDispatcherClientForSend() *DispatcherClient {
	dispatcherClient := dcm.getDispatcherClient()
	return dispatcherClient
}

// serve the dispatcher client, receive RESPs from dispatcher and process
func (dcm *DispatcherConnMgr) serveDispatcherClient() {
	gwlog.Debugf("%s.serveDispatcherClient: start serving dispatcher client ...", dcm)
	for {
		dc := dcm.assureConnected()
		var msgtype proto.MsgType
		pkt, err := dc.Recv(&msgtype)

		if err != nil {
			if gwioutil.IsTimeoutError(err) {
				continue
			}

			gwlog.TraceError("serveDispatcherClient: RecvMsgPacket error: %s", err.Error())
			dc.Close()
			dcm.delegate.HandleDispatcherClientDisconnect()
			time.Sleep(_LOOP_DELAY_ON_DISPATCHER_CLIENT_ERROR)
			continue
		}

		if consts.DEBUG_PACKETS {
			gwlog.Debugf("%s.RecvPacket: msgtype=%v, payload=%v", dc, msgtype, pkt.Payload())
		}
		dcm.delegate.HandleDispatcherClientPacket(msgtype, pkt)
	}
}