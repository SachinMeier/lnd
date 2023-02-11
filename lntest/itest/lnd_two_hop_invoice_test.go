package itest

import (
	"bytes"
	"context"
	"encoding/hex"
	"github.com/btcsuite/btcutil"
	"github.com/davecgh/go-spew/spew"
	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lntest"
	"github.com/lightningnetwork/lnd/lntest/wait"
	"time"
)

func testTwoHopsThisTime(net *lntest.NetworkHarness, t *harnessTest) {
	ctxb := context.Background()

	carol, err := net.NewNode("Carol", nil)
	if err != nil {
		t.Fatalf("failed to setup new node for carol: %v", err)
	}

	err = net.EnsureConnected(ctxb, carol, net.Bob)
	if err != nil {
		t.Fatalf("failed to connect carol and bob: %v", err)
	}

	// Open a channel with 100k satoshis between Alice and Bob with Alice being
	// the sole funder of the channel.
	ctxt, _ := context.WithTimeout(ctxb, channelOpenTimeout)
	chanAmt := btcutil.Amount(100000)
	abChanPoint := openChannelAndAssert(
		ctxt, t, net, net.Alice, net.Bob,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	bcChanPoint := openChannelAndAssert(
		ctxt, t, net, net.Bob, carol,
		lntest.OpenChannelParams{
			Amt: chanAmt,
		},
	)

	// set Bob->Carol channel fees to zero so that we can reuse the assertAmountSent func
	_, err = net.Bob.UpdateChannelPolicy(ctxb, &lnrpc.PolicyUpdateRequest{
		Scope:         &lnrpc.PolicyUpdateRequest_Global{Global: true},
		BaseFeeMsat:   0,
		FeeRate:       0,
		TimeLockDelta: 18,
	})
	if err != nil {
		t.Fatalf("failed to set bob & carol channel fees to zero: %v", err)
	}

	// Now that the channel is open, create an invoice for Carol which
	// expects a payment of 1000 satoshis from Alice paid via a particular
	// preimage.
	const paymentAmt = 1000
	preimage := bytes.Repeat([]byte("A"), 32)
	invoice := &lnrpc.Invoice{
		Memo:      "testing",
		RPreimage: preimage,
		Value:     paymentAmt,
	}
	invoiceResp, err := carol.AddInvoice(ctxb, invoice)
	if err != nil {
		t.Fatalf("unable to add invoice: %v", err)
	}

	// Wait for Alice & Bob to recognize and advertise their new channels generated
	// above.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)

	// Channel Alice<>Bob
	err = net.Alice.WaitForNetworkChannelOpen(ctxt, abChanPoint)
	if err != nil {
		t.Fatalf("alice didn't advertise channel before "+
			"timeout: %v", err)
	}
	err = net.Bob.WaitForNetworkChannelOpen(ctxt, abChanPoint)
	if err != nil {
		t.Fatalf("alice didn't advertise channel before "+
			"timeout: %v", err)
	}

	// Channel Bob<>Carol
	err = net.Bob.WaitForNetworkChannelOpen(ctxt, bcChanPoint)
	if err != nil {
		t.Fatalf("bob didn't advertise channel before "+
			"timeout: %v", err)
	}
	err = carol.WaitForNetworkChannelOpen(ctxt, bcChanPoint)
	if err != nil {
		t.Fatalf("bob didn't advertise channel before "+
			"timeout: %v", err)
	}

	// With the invoice for Carol added, send a payment towards Alice paying
	// to the above generated invoice.
	ctxt, _ = context.WithTimeout(ctxb, defaultTimeout)
	resp := sendAndAssertSuccess(
		ctxt, t, net.Alice,
		&routerrpc.SendPaymentRequest{
			PaymentRequest: invoiceResp.PaymentRequest,
			TimeoutSeconds: 60,
			FeeLimitMsat:   noFeeLimitMsat,
		},
	)
	if hex.EncodeToString(preimage) != resp.PaymentPreimage {
		t.Fatalf("preimage mismatch: expected %v, got %v", preimage,
			resp.PaymentPreimage)
	}

	// Carol's invoice should now be found and marked as settled.
	payHash := &lnrpc.PaymentHash{
		RHash: invoiceResp.RHash,
	}
	ctxt, _ = context.WithTimeout(ctxt, defaultTimeout)
	dbInvoice, err := carol.LookupInvoice(ctxt, payHash)
	if err != nil {
		t.Fatalf("unable to lookup invoice: %v", err)
	}
	if dbInvoice.State != lnrpc.Invoice_SETTLED {
		t.Fatalf("bob's invoice should be marked as settled: %v",
			spew.Sdump(dbInvoice))
	}

	err = wait.NoError(
		assertAmountSent(paymentAmt, net.Alice, carol),
		3*time.Second,
	)
	if err != nil {
		t.Fatalf(err.Error())
	}

	err = wait.NoError(
		assertAmountRouted(paymentAmt, net.Bob, net.Alice, carol),
		3*time.Second,
	)
	if err != nil {
		t.Fatalf(err.Error())
	}

	ctxt, _ = context.WithTimeout(ctxb, channelCloseTimeout)
	closeChannelAndAssert(ctxt, t, net, net.Alice, abChanPoint, false)
	closeChannelAndAssert(ctxt, t, net, net.Bob, bcChanPoint, false)
}
