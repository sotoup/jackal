/*
 * Copyright (c) 2018 Miguel Ángel Ortuño.
 * See the LICENSE file for more information.
 */

package module

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ortuman/jackal/config"
	"github.com/ortuman/jackal/log"
	"github.com/ortuman/jackal/stream/c2s"
	"github.com/ortuman/jackal/stream/errors"
	"github.com/ortuman/jackal/xml"
	"github.com/pborman/uuid"
)

const pingNamespace = "urn:xmpp:ping"

// XEPPing represents a ping server stream module.
type XEPPing struct {
	cfg  *config.ModPing
	strm c2s.Stream

	pingTm *time.Timer
	pongCh chan struct{}

	pingMu sync.RWMutex // guards 'pingID'
	pingId string

	waitingPing uint32
	pingOnce    sync.Once
}

// NewXEPPing returns an ping IQ handler module.
func NewXEPPing(config *config.ModPing, strm c2s.Stream) *XEPPing {
	return &XEPPing{
		cfg:    config,
		strm:   strm,
		pongCh: make(chan struct{}, 1),
	}
}

// AssociatedNamespaces returns namespaces associated
// with ping module.
func (x *XEPPing) AssociatedNamespaces() []string {
	return []string{pingNamespace}
}

// Done signals stream termination.
func (x *XEPPing) Done() {
}

// MatchesIQ returns whether or not an IQ should be
// processed by the ping module.
func (x *XEPPing) MatchesIQ(iq *xml.IQ) bool {
	return x.isPongIQ(iq) || iq.FindElementNamespace("ping", pingNamespace) != nil
}

// ProcessIQ processes a ping IQ taking according actions
// over the associated stream.
func (x *XEPPing) ProcessIQ(iq *xml.IQ) {
	if x.isPongIQ(iq) {
		x.handlePongIQ(iq)
		return
	}
	toJid := iq.ToJID()
	if toJid.Node() != x.strm.Username() {
		x.strm.SendElement(iq.ForbiddenError())
		return
	}
	p := iq.FindElementNamespace("ping", pingNamespace)
	if p == nil || p.ElementsCount() > 0 {
		x.strm.SendElement(iq.BadRequestError())
		return
	}
	log.Infof("received ping... id: %s", iq.ID())
	if iq.IsGet() {
		log.Infof("sent pong... id: %s", iq.ID())
		x.strm.SendElement(iq.ResultIQ())
	} else {
		x.strm.SendElement(iq.BadRequestError())
	}
}

// StartPinging starts pinging peer every 'send interval' period.
func (x *XEPPing) StartPinging() {
	if x.cfg.Send {
		x.pingOnce.Do(func() {
			x.pingTm = time.AfterFunc(time.Second*time.Duration(x.cfg.SendInterval), x.sendPing)
		})
	}
}

// ResetDeadline resets send ping deadline.
func (x *XEPPing) ResetDeadline() {
	if x.cfg.Send && atomic.LoadUint32(&x.waitingPing) == 1 {
		x.pingTm.Reset(time.Second * time.Duration(x.cfg.SendInterval))
		return
	}
}

func (x *XEPPing) isPongIQ(iq *xml.IQ) bool {
	x.pingMu.RLock()
	defer x.pingMu.RUnlock()
	return x.pingId == iq.ID() && (iq.IsResult() || iq.IsError())
}

func (x *XEPPing) sendPing() {
	atomic.StoreUint32(&x.waitingPing, 0)

	x.pingMu.Lock()
	x.pingId = uuid.New()
	pingId := x.pingId
	x.pingMu.Unlock()

	iq := xml.NewIQType(pingId, xml.GetType)
	iq.SetTo(x.strm.JID().String())
	iq.AppendElement(xml.NewElementNamespace("ping", pingNamespace))

	x.strm.SendElement(iq)

	log.Infof("sent ping... id: %s", pingId)

	x.waitForPong()
}

func (x *XEPPing) waitForPong() {
	t := time.NewTimer(time.Second * time.Duration(x.cfg.SendInterval))
	select {
	case <-x.pongCh:
		return
	case <-t.C:
		x.strm.Disconnect(streamerror.ErrConnectionTimeout)
	}
}

func (x *XEPPing) handlePongIQ(iq *xml.IQ) {
	log.Infof("received pong... id: %s", iq.ID())

	x.pingMu.Lock()
	x.pingId = ""
	x.pingMu.Unlock()

	x.pongCh <- struct{}{}
	x.pingTm.Reset(time.Second * time.Duration(x.cfg.SendInterval))
	atomic.StoreUint32(&x.waitingPing, 1)
}
