package kontrol

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/koding/kite"
	kontrolprotocol "github.com/koding/kite/kontrol/protocol"
	"github.com/koding/kite/protocol"
)

func (k *Kontrol) handleHeartbeat(rw http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")

	k.clientsMu.Lock()
	defer k.clientsMu.Unlock()

	if updateTimer, ok := k.clients[id]; ok {
		// try to reset the timer every time the remote kite sends sends us a
		// heartbeat. Because the timer get reset, the timer never be fired, so
		// the value get always updated with the updater in the background
		// according to the write interval. If the kite doesn't send any
		// heartbeat, the timer func is being called, which stops the updater
		// so the key is being deleted automatically via the TTL mechanism.
		updateTimer.Reset(HeartbeatInterval + HeartbeatDelay)
		rw.Write([]byte("pong"))
		return
	}

	// if we reach here than it means kontrol is restarted and we getting still
	// heartbeats from kites, so send back "registeragain" to invoke a
	// registration from their side again.

	rw.Write([]byte("registeragain"))
	return

}

func (k *Kontrol) handleRegister(r *kite.Request) (interface{}, error) {
	k.log.Info("Register request from: %s", r.Client.Kite)

	if r.Args.One().MustMap()["url"].MustString() == "" {
		return nil, errors.New("invalid url")
	}

	var args struct {
		URL string `json:"url"`
	}
	r.Args.One().MustUnmarshal(&args)
	if args.URL == "" {
		return nil, errors.New("empty url")
	}

	// Only accept requests with kiteKey because we need this info
	// for generating tokens for this kite.
	if r.Auth.Type != "kiteKey" {
		return nil, fmt.Errorf("Unexpected authentication type: %s", r.Auth.Type)
	}

	kiteURL := args.URL
	remote := r.Client

	if err := validateKiteKey(&remote.Kite); err != nil {
		return nil, err
	}

	value := &kontrolprotocol.RegisterValue{
		URL: kiteURL,
	}

	// Register first by adding the value to the storage. Return if there is
	// any error.
	if err := k.storage.Upsert(&remote.Kite, value); err != nil {
		k.log.Error("storage add '%s' error: %s", remote.Kite, err)
		return nil, errors.New("internal error - register")
	}

	// we create a new ticker which is going to update the key periodically in
	// the storage so it's always up to date. Instead of updating the key
	// periodically according to the HeartBeatInterval below, we are buffering
	// the write speed here with the UpdateInterval.
	updater := time.NewTicker(UpdateInterval)
	updaterFunc := func() {
		for _ = range updater.C {
			k.log.Debug("Kite is active, updating the value %s", remote.Kite)
			err := k.storage.Update(&remote.Kite, value)
			if err != nil {
				k.log.Error("storage update '%s' error: %s", remote.Kite, err)
			}
		}
	}
	go updaterFunc()

	// lostFunc is called when we don't get any heartbeat or we lost
	// connection. In any case it will stop the updater
	lostFunc := func() {
		k.log.Info("Kite didn't get heartbeat. Stopping the updater %s",
			remote.Kite)
		// stop the updater so it doesn't update it in the background
		updater.Stop()

		k.clientsMu.Lock()
		delete(k.clients, remote.Kite.ID)
		k.clientsMu.Unlock()
	}

	// we are now creating a timer that is going to call the lostFunc, which
	// stops the background updater if it's not resetted.
	k.clientsMu.Lock()
	k.clients[remote.Kite.ID] = time.AfterFunc(HeartbeatInterval+HeartbeatDelay,
		lostFunc)
	k.clientsMu.Unlock()

	k.log.Info("Kite registered: %s", remote.Kite)

	// send response back to the kite, also identify him with the new name
	return &protocol.RegisterResult{
		URL:               args.URL,
		HeartbeatInterval: int64(HeartbeatInterval / time.Second),
	}, nil
}

func (k *Kontrol) handleGetKites(r *kite.Request) (interface{}, error) {
	// This type is here until inversion branch is merged.
	// Reason: We can't use the same struct for marshaling and unmarshaling.
	// TODO use the struct in protocol
	type GetKitesArgs struct {
		Query *protocol.KontrolQuery `json:"query"`
	}

	var args GetKitesArgs
	r.Args.One().MustUnmarshal(&args)

	query := args.Query

	// audience will go into the token as "aud" claim.
	audience := getAudience(query)

	// Generate token once here because we are using the same token for every
	// kite we return and generating many tokens is really slow.
	token, err := generateToken(audience, r.Username,
		k.Kite.Kite().Username, k.privateKey)
	if err != nil {
		return nil, err
	}

	// Get kites from the storage
	kites, err := k.storage.Get(query)
	if err != nil {
		return nil, err
	}

	// Attach tokens to kites
	kites.Attach(token)

	return &protocol.GetKitesResult{
		Kites: kites,
	}, nil
}

func (k *Kontrol) handleGetToken(r *kite.Request) (interface{}, error) {
	var query *protocol.KontrolQuery
	err := r.Args.One().Unmarshal(&query)
	if err != nil {
		return nil, errors.New("Invalid query")
	}

	// check if it's exist
	kites, err := k.storage.Get(query)
	if err != nil {
		return nil, err
	}

	if len(kites) > 1 {
		return nil, errors.New("query matches more than one kite")
	}

	audience := getAudience(query)

	return generateToken(audience, r.Username, k.Kite.Kite().Username, k.privateKey)
}

func (k *Kontrol) handleMachine(r *kite.Request) (interface{}, error) {
	if k.MachineAuthenticate != nil {
		if err := k.MachineAuthenticate(r); err != nil {
			return nil, errors.New("cannot authenticate user")
		}
	}

	username := r.Args.One().MustString() // username should be send as an argument
	return k.registerUser(username)
}
