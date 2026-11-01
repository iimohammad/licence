package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tabloy/keygate/internal/model"
)

// PaymentGateway is the seam between Keygate's licence machinery and
// whoever actually moves money (issue #53). Stripe is the only
// implementation today — Stripe is invite-only in several markets, so
// the seam exists so a second backend (Razorpay, Paddle, ...) can
// slot in behind the same routes and background jobs without touching
// fulfilment code.
//
// Adding one: implement every method against your provider's API,
// register the name in NewGateway, and set PAYMENT_GATEWAY to it.
// The hard part is not checkout — it is subscriptions, renewals,
// refunds and webhooks staying consistent with what fulfilment
// expects. StripeHandler and its tests are the contract: a new
// gateway should keep the same observable effects (metadata keys,
// webhook-driven fulfilment idempotency, cancel-state sync).
type PaymentGateway interface {
	// HTTP endpoints.
	CheckoutByPlan(c *gin.Context)          // GET  /pay/:checkout_id
	VerifyCheckoutSession(c *gin.Context)   // POST /checkout/verify
	Webhook(c *gin.Context)                 // POST /webhook/{gateway}
	StartUpgrade(c *gin.Context)            // portal: pay for a plan upgrade
	ChangePlan(c *gin.Context)              // portal: choose a new plan
	PreviewSubscriptionChange(c *gin.Context)
	ChangeSubscription(c *gin.Context)
	PendingSubscriptionChange(c *gin.Context)
	CancelSubscription(c *gin.Context)
	CreatePortalSession(c *gin.Context)     // provider's billing portal
	ListInvoices(c *gin.Context)
	RenewUpdates(c *gin.Context)            // one-time renewal of an update period

	// Background reconciliation. Called on the server's timers; each
	// one reconciles what the provider says with what the database
	// holds so a missed webhook never silently drops a sale.
	SyncRecentCheckouts(ctx context.Context)
	SyncPendingCheckouts(ctx context.Context)
	SyncCancelStates(ctx context.Context)
	SyncLapsedTermHolds(ctx context.Context)
	SetupWebhookEndpoint(ctx context.Context)
	RetireReplacedEndpoint(ctx context.Context)

	// Licence lifecycle hooks.
	UpgradableLicenses(ctx context.Context, email string, ls []*model.License) (map[string]bool, error)
	SubscriptionEnded(ctx context.Context, subscriptionID string) (bool, error)
	CancelForAdmin(ctx context.Context, lic *model.License) (reverted bool, err error)
	FollowSubscription(ctx context.Context, lic *model.License) (func(context.Context) error, error)
	EndPastDueUpgrade(ctx context.Context, lic *model.License, threshold time.Time) (bool, error)
	EndGraceExpiredUpgrade(ctx context.Context, lic *model.License, cutoff time.Time) (bool, error)
}

// Compile-time proof that Stripe satisfies the whole seam.
var _ PaymentGateway = (*StripeHandler)(nil)

// GatewayStripe is the only built-in payment backend.
const GatewayStripe = "stripe"

// NewGateway picks the payment backend by name. An unknown name fails
// at startup rather than at the first sale.
func NewGateway(name string, stripe *StripeHandler) (PaymentGateway, error) {
	switch name {
	case "", GatewayStripe:
		return stripe, nil
	default:
		return nil, fmt.Errorf("unknown PAYMENT_GATEWAY %q: built-in gateways are %q", name, GatewayStripe)
	}
}
