package payment

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/stripe/stripe-go/v82"
	taxrate "github.com/stripe/stripe-go/v82/taxrate"

	"github.com/tabloy/keygate/internal/store"
)

// How Stripe Checkout Sessions collect tax (issue #48). Sellers with
// tax configured in Stripe still sold tax-free because Keygate's
// sessions never asked for it — there is no account-wide default for
// API-created sessions.
//
//	off        today's behaviour: no tax on Keygate sales (default)
//	fixed      attach the account's active Tax Rates to every line
//	           item (dynamic_tax_rates): "VAT 20%, UK" style rates the
//	           operator created under Stripe → Tax Rates
//	automatic  Stripe Tax computes from the account's registrations
//	           (automatic_tax[enabled], Stripe bills per calculation)
//
// Any mode but off also requires the billing address (Stripe picks
// the rate from the buyer's country), collects tax IDs so business
// buyers can enter a VAT/GST/ABN number (reverse charge), and lets an
// existing customer's name and address be updated, which that needs.
// The rates, who pays and whether they are included in the price all
// stay in Stripe; this setting only says "ask for it".
const (
	SettingTaxMode    = "stripe_tax_mode"
	TaxModeOff        = "off"
	TaxModeFixedRates = "fixed"
	TaxModeAutomatic  = "automatic"
)

// taxModeSetting reads the operator's choice. A missing row is "off":
// an install that never touched the setting keeps selling exactly as
// it did.
func taxModeSetting(ctx context.Context, s *store.Store) string {
	v, err := s.GetSetting(ctx, SettingTaxMode)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("stripe tax: reading setting failed, selling without tax", "error", err)
		}
		return TaxModeOff
	}
	switch v {
	case TaxModeFixedRates, TaxModeAutomatic:
		return v
	}
	return TaxModeOff
}

// applyTaxToSession sets the tax fields on a session about to be
// created. An error means the checkout must be refused, not sold
// tax-free: the operator asked for tax and something in the way is
// broken, and quietly dropping it is the bug this whole file fixes.
func (h *StripeHandler) applyTaxToSession(ctx context.Context, params *stripe.CheckoutSessionParams) error {
	switch taxModeSetting(ctx, h.Store) {
	case TaxModeAutomatic:
		params.AutomaticTax = &stripe.CheckoutSessionAutomaticTaxParams{
			Enabled: stripe.Bool(true),
		}
	case TaxModeFixedRates:
		ids, err := activeTaxRateIDs()
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return errors.New("stripe tax: no active tax rates in the account; create them under Stripe → Tax Rates, or pick a different tax mode")
		}
		for _, item := range params.LineItems {
			item.DynamicTaxRates = ids
		}
	default:
		return nil
	}

	params.BillingAddressCollection = stripe.String("required")
	params.TaxIDCollection = &stripe.CheckoutSessionTaxIDCollectionParams{
		Enabled: stripe.Bool(true),
	}
	// customer_update is only accepted together with customer; the
	// email-only sessions get their address collected fresh.
	if params.Customer != nil {
		params.CustomerUpdate = &stripe.CheckoutSessionCustomerUpdateParams{
			Address: stripe.String("auto"),
			Name:    stripe.String("auto"),
		}
	}
	return nil
}

// activeTaxRateIDs lists the account's active Tax Rates for the
// "fixed" mode. nil entries in the result cannot happen: the Stripe
// API returns fully materialized objects.
func activeTaxRateIDs() ([]*string, error) {
	iter := taxrate.List(&stripe.TaxRateListParams{
		Active: stripe.Bool(true),
		ListParams: stripe.ListParams{Limit: stripe.Int64(100)},
	})
	ids := make([]*string, 0, 8)
	for iter.Next() {
		tr := iter.TaxRate()
		if tr != nil {
			ids = append(ids, stripe.String(tr.ID))
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}
