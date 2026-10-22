package cli

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/barad-io/barad/wire"
)

// Billing and usage. Barad charges for workspace time from prepaid
// credits; model providers bill inference themselves.

func money(nanos int64) string {
	sign := ""
	if nanos < 0 {
		sign, nanos = "-", -nanos
	}
	return fmt.Sprintf("%s$%.2f", sign, float64(nanos)/1e9)
}

var billingStatus = command{
	usage: "billing [status]",
	about: "your credits, what workspace time costs, and the terms",
	run: func(c *call) error {
		b, err := c.api.Billing(c.ctx)
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(b)
		}
		terms := "accepted"
		if !b.TermsAccepted {
			terms = "not accepted yet: barad billing terms --accept"
		}
		fmt.Fprintf(c.out, "Credits     %s left\nWorkspaces  %s an hour while awake; asleep they are free\nTerms       %s, %s\n            %s\n",
			money(b.BalanceUSDNanos), money(b.WorkspaceRateUSDNanosPerHour), b.TermsVersion, terms, b.TermsURL)
		return nil
	},
}

var billingHistory = command{
	usage: "billing history [--all]",
	about: "what came into and went out of your credits, newest first (--all: every page)",
	flags: func(fs *flag.FlagSet, c *call) { boolFlag(fs, c, "all", "every entry, not just the newest 50") },
	run: func(c *call) error {
		var all []wire.BillingEntry
		cursor := ""
		for {
			page, err := c.api.BillingHistory(c.ctx, cursor)
			if err != nil {
				return err
			}
			all = append(all, page.Entries...)
			if cursor = page.Cursor; cursor == "" || !c.flag("all") {
				break
			}
		}
		var rows [][]string
		for _, e := range all {
			rows = append(rows, []string{e.EffectiveAt.Local().Format("2 Jan 2006 15:04"), entryKind(e.Kind), money(e.AmountUSDNanos), e.SessionID})
		}
		return c.table(all, []string{"WHEN", "WHAT", "AMOUNT", "SESSION"}, rows)
	},
}

func entryKind(k string) string {
	if s, ok := map[string]string{"account_initialized": "account opened", "workspace_runtime": "workspace time", "purchase": "credits bought",
		"refund": "refund", "chargeback": "chargeback", "chargeback_credit": "chargeback returned", "promotional_grant": "free credits", "adjustment": "adjustment"}[k]; ok {
		return s
	}
	return k
}

var billingBuy = command{
	usage: "billing buy AMOUNT [--accept-terms]",
	about: "buy credits (AMOUNT in US dollars, like 25): pay in your browser, then the credits arrive here",
	flags: func(fs *flag.FlagSet, c *call) {
		boolFlag(fs, c, "accept-terms", "accept the current Terms of Service without asking")
	},
	run: func(c *call) error {
		amount, err := c.arg(0, "the amount, in US dollars")
		if err != nil {
			return err
		}
		dollars, err := strconv.ParseFloat(strings.TrimPrefix(strings.TrimSpace(amount), "$"), 64)
		if err != nil || dollars <= 0 {
			return fmt.Errorf("%q is not an amount of dollars", amount)
		}
		cents := int64(math.Round(dollars * 100))
		b, err := c.api.Billing(c.ctx)
		if err != nil {
			return err
		}
		if cents < b.MinimumPurchaseUSDCents || cents > b.MaximumPurchaseUSDCents {
			return fmt.Errorf("buy between %s and %s", money(b.MinimumPurchaseUSDCents*1e7), money(b.MaximumPurchaseUSDCents*1e7))
		}
		if err := c.acceptTerms(b, c.flag("accept-terms")); err != nil {
			return err
		}
		out, err := c.api.Checkout(c.ctx, "cli_"+strings.TrimPrefix(requestID(), "msg_"), cents, b.TermsVersion)
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(out)
		}
		fmt.Fprintf(c.out, "Pay %s in your browser. Barad starts serving you as soon as you pay.\n", money(cents*1e7))
		c.browse(out.URL)
		for until := time.Now().Add(30 * time.Minute); time.Now().Before(until); {
			select {
			case <-c.ctx.Done():
				return c.ctx.Err()
			case <-time.After(pollEvery):
			}
			now, err := c.api.Billing(c.ctx)
			if err != nil {
				return err
			}
			if now.BalanceUSDNanos > b.BalanceUSDNanos {
				fmt.Fprintf(c.out, "Paid: you have %s of credits.\n", money(now.BalanceUSDNanos))
				return nil
			}
		}
		return errors.New("no payment arrived in 30 minutes; barad billing shows your credits")
	},
}

// acceptTerms makes sure the current terms are accepted: by flag, or by
// asking.
func (c *call) acceptTerms(b *wire.BillingAccount, yes bool) error {
	if b.TermsAccepted {
		return nil
	}
	if !yes {
		if err := c.confirm(fmt.Sprintf("Accept Barad's Terms of Service (%s, %s)?", b.TermsVersion, b.TermsURL)); err != nil {
			return err
		}
	}
	return c.api.AcceptTerms(c.ctx, b.TermsVersion)
}

var billingTerms = command{
	usage: "billing terms [--accept]",
	about: "the Terms of Service credits are bought under, and accepting them",
	flags: func(fs *flag.FlagSet, c *call) { boolFlag(fs, c, "accept", "accept the current version") },
	run: func(c *call) error {
		b, err := c.api.Billing(c.ctx)
		if err != nil {
			return err
		}
		if c.flag("accept") {
			if err := c.acceptTerms(b, true); err != nil {
				return err
			}
			b.TermsAccepted = true
		}
		state := "you accepted them"
		if !b.TermsAccepted {
			state = "not accepted yet: barad billing terms --accept"
		}
		return c.say(b, "Terms of Service %s: %s\n%s", b.TermsVersion, state, b.TermsURL)
	},
}

// Usage.

func daysFlag(fs *flag.FlagSet, c *call) { strFlag(fs, c, "days", "7, 30 or 90 (default 30)") }

func (c *call) days() (int, error) {
	switch c.str("days") {
	case "", "30":
		return 30, nil
	case "7":
		return 7, nil
	case "90":
		return 90, nil
	}
	return 0, errors.New("--days is 7, 30 or 90")
}

func timeZone() string {
	if tz := time.Local.String(); tz != "Local" {
		return tz
	}
	return "UTC"
}

func tokens(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func minutes(m int64) string { return fmt.Sprintf("%dh %02dm", m/60, m%60) }

var usageSummary = command{
	usage: "usage [summary] [--days 7|30|90]",
	about: "what you used: Barad's workspace time (paid with credits) and your providers' tokens",
	flags: daysFlag,
	run: func(c *call) error {
		days, err := c.days()
		if err != nil {
			return err
		}
		ws, err := c.api.WorkspaceUsage(c.ctx, days, timeZone())
		if err != nil {
			return err
		}
		inf, err := c.api.Usage(c.ctx, days, timeZone())
		if err != nil {
			return err
		}
		b, err := c.api.Billing(c.ctx)
		if err != nil {
			return err
		}
		if c.asJSON {
			return c.json(map[string]any{"days": days, "barad": ws, "inference": inf, "credits_usd_nanos": b.BalanceUSDNanos})
		}
		t, m := ws.Totals, inf.Totals
		fmt.Fprintf(c.out, "The last %d days\n\nBARAD       %s of credits for %s of workspace time, %s  ·  %s left\nINFERENCE   %s tokens  ·  %s direct API cost  ·  %s at API prices (billed by your providers)\n",
			days, money(t.ChargedUSDNanos), minutes(t.BilledMinutes), count(int(t.Runs), "run"), money(b.BalanceUSDNanos),
			tokens(m.ProcessedTokens), money(m.ActualCost.Nanodollars()), money(m.APIEquivalentCost.Nanodollars()))
		fmt.Fprintln(c.out, "\nMore: barad usage sessions | providers | models | daily")
		return nil
	},
}

var usageSessions = command{
	usage: "usage sessions [--days 7|30|90]",
	about: "workspace time by session",
	flags: daysFlag,
	run: func(c *call) error {
		days, err := c.days()
		if err != nil {
			return err
		}
		ws, err := c.api.WorkspaceUsage(c.ctx, days, timeZone())
		if err != nil {
			return err
		}
		all, err := c.api.Sessions(c.ctx)
		if err != nil {
			return err
		}
		names := map[string]string{}
		for _, s := range all {
			names[s.SessionID] = sessionName(s)
		}
		var rows [][]string
		for _, w := range ws.Workspaces {
			rows = append(rows, []string{firstNonEmpty(names[w.SessionID], "a deleted session"), minutes(w.BilledMinutes), money(w.ChargedUSDNanos), fmt.Sprint(w.Runs), ago(w.LastUsedAt), w.SessionID})
		}
		return c.table(ws.Workspaces, []string{"SESSION", "TIME", "CREDITS", "RUNS", "LAST", "ID"}, rows)
	},
}

func usageBuckets(models bool) command {
	verb, what := "providers", "provider connection"
	if models {
		verb, what = "models", "model"
	}
	return command{
		usage: "usage " + verb + " [--days 7|30|90]",
		about: "tokens and their cost by " + what,
		flags: daysFlag,
		run: func(c *call) error {
			days, err := c.days()
			if err != nil {
				return err
			}
			r, err := c.api.Usage(c.ctx, days, timeZone())
			if err != nil {
				return err
			}
			list := r.Providers
			if models {
				list = r.Models
			}
			var rows [][]string
			for _, b := range list {
				name := firstNonEmpty(b.ProviderResourceName, string(b.Provider))
				if models {
					name = b.Model
				}
				rows = append(rows, []string{name, tokens(b.Metrics.ProcessedTokens), money(b.Metrics.ActualCost.Nanodollars()), money(b.Metrics.APIEquivalentCost.Nanodollars())})
			}
			return c.table(list, []string{strings.ToUpper(what), "TOKENS", "DIRECT COST", "AT API PRICES"}, rows)
		},
	}
}

var usageProviders, usageModels = usageBuckets(false), usageBuckets(true)

var usageDaily = command{
	usage: "usage daily [--days 7|30|90]",
	about: "each day's workspace time, credits and tokens",
	flags: daysFlag,
	run: func(c *call) error {
		days, err := c.days()
		if err != nil {
			return err
		}
		ws, err := c.api.WorkspaceUsage(c.ctx, days, timeZone())
		if err != nil {
			return err
		}
		inf, err := c.api.Usage(c.ctx, days, timeZone())
		if err != nil {
			return err
		}
		byDay := map[string]wire.UsageMetrics{}
		for _, d := range inf.Daily {
			byDay[d.Date] = d.Metrics
		}
		type day struct {
			Date      string             `json:"date"`
			Barad     wire.WorkspaceTime `json:"barad"`
			Inference wire.UsageMetrics  `json:"inference"`
		}
		var out []day
		var rows [][]string
		for _, d := range ws.Daily {
			m := byDay[d.Date]
			out = append(out, day{d.Date, d.WorkspaceTime, m})
			rows = append(rows, []string{d.Date, minutes(d.BilledMinutes), money(d.ChargedUSDNanos), tokens(m.ProcessedTokens), money(m.ActualCost.Nanodollars())})
		}
		return c.table(out, []string{"DAY", "WORKSPACE TIME", "CREDITS", "TOKENS", "DIRECT COST"}, rows)
	},
}
