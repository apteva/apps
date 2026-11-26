package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Policy is the rule set every outgoing transfer is checked against. Agents
// have no tool that changes it; only the panel (an authenticated person)
// writes a new version.
type Policy struct {
	Version                int      `json:"version"`
	PerTxUSDMicros         int64    `json:"per_tx_usd_micros"`
	DailyUSDMicros         int64    `json:"daily_usd_micros"`
	ApprovalUSDMicros      int64    `json:"approval_usd_micros"`
	Tokens                 []string `json:"tokens"`
	NewRecipientDelayHours int      `json:"new_recipient_delay_hours"`
	UpdatedBy              int64    `json:"updated_by"`
	CreatedAt              string   `json:"created_at"`
}

// Defaults from the proposal; placeholders until the team picks real ones.
func defaultPolicy() Policy {
	return Policy{
		PerTxUSDMicros:         100_000_000,
		DailyUSDMicros:         250_000_000,
		ApprovalUSDMicros:      25_000_000,
		Tokens:                 []string{"USDC"},
		NewRecipientDelayHours: 24,
	}
}

// Hard ceilings a policy edit cannot exceed, so a typo in the panel can't
// turn a $100 cap into a $100,000 one.
const (
	maxPerTxUSDMicros   = 10_000_000_000 // $10,000
	maxDailyUSDMicros   = 25_000_000_000 // $25,000
	maxRecipientDelay   = 24 * 30
	maxPendingPerWallet = 10
)

func (p Policy) validate(chain *chainInfo) error {
	switch {
	case p.PerTxUSDMicros <= 0 || p.DailyUSDMicros <= 0:
		return errors.New("caps must be greater than zero")
	case p.PerTxUSDMicros > maxPerTxUSDMicros:
		return fmt.Errorf("per-transfer cap cannot exceed $%d", maxPerTxUSDMicros/1_000_000)
	case p.DailyUSDMicros > maxDailyUSDMicros:
		return fmt.Errorf("daily cap cannot exceed $%d", maxDailyUSDMicros/1_000_000)
	case p.PerTxUSDMicros > p.DailyUSDMicros:
		return errors.New("per-transfer cap cannot be larger than the daily cap")
	case p.ApprovalUSDMicros < 0:
		return errors.New("approval threshold cannot be negative")
	case p.NewRecipientDelayHours < 0 || p.NewRecipientDelayHours > maxRecipientDelay:
		return fmt.Errorf("new-recipient delay must be between 0 and %d hours", maxRecipientDelay)
	}
	for _, t := range p.Tokens {
		if _, err := chain.token(t); err != nil {
			return err
		}
	}
	return nil
}

func (p Policy) allowsToken(symbol string) bool {
	for _, t := range p.Tokens {
		if strings.EqualFold(t, symbol) {
			return true
		}
	}
	return false
}

func (p Policy) tokensJSON() string {
	b, _ := json.Marshal(p.Tokens)
	return string(b)
}

// ─── Evaluation ──────────────────────────────────────────────────────

type recipientState struct {
	Found       bool
	Status      string
	UsableAfter time.Time
	Label       string
}

type evalInput struct {
	Frozen          bool
	Policy          Policy
	Token           string
	USDMicros       int64
	Recipient       recipientState
	Spent24h        int64 // USD micros already sent or sending in the last 24 hours
	Pending         int   // transfers already waiting for approval
	Now             time.Time
	AlreadyApproved bool // true when re-checking at execution after a person approved
}

type verdict struct {
	Outcome string `json:"outcome"` // execute | needs_approval | refused
	Rule    string `json:"rule,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

func refused(rule, detail string) verdict {
	return verdict{Outcome: "refused", Rule: rule, Detail: detail}
}

func usd(micros int64) string {
	return fmt.Sprintf("$%d.%02d", micros/1_000_000, (micros%1_000_000)/10_000)
}

// evaluate applies the rules in a fixed order and names the first one that
// fails. The order puts cheap, absolute rules (frozen, token, recipient)
// before money rules so the refusal an agent sees is the most useful one.
func evaluate(in evalInput) verdict {
	p := in.Policy
	if in.Frozen {
		return refused("frozen", "the wallet is frozen; a person must unfreeze it in the panel")
	}
	if !p.allowsToken(in.Token) {
		return refused("token_not_allowed", fmt.Sprintf("%s transfers are not allowed by this wallet's policy", strings.ToUpper(in.Token)))
	}
	switch {
	case !in.Recipient.Found || in.Recipient.Status == "removed":
		return refused("recipient_not_allowed", "the recipient is not on the allowlist; propose it with crypto_wallet_recipient_propose")
	case in.Recipient.Status == "pending":
		return refused("recipient_not_allowed", "the recipient is waiting for a person to approve it")
	case in.Recipient.Status != "approved":
		return refused("recipient_not_allowed", "a person rejected this recipient")
	case in.Now.Before(in.Recipient.UsableAfter):
		return refused("recipient_cooling_off", "new recipients can receive funds from "+in.Recipient.UsableAfter.UTC().Format(time.RFC3339))
	}
	if in.USDMicros > p.PerTxUSDMicros {
		return refused("per_transfer_cap", fmt.Sprintf("%s is above the per-transfer cap of %s", usd(in.USDMicros), usd(p.PerTxUSDMicros)))
	}
	if in.Spent24h+in.USDMicros > p.DailyUSDMicros {
		left := p.DailyUSDMicros - in.Spent24h
		if left < 0 {
			left = 0
		}
		return refused("daily_cap", fmt.Sprintf("only %s of the %s daily cap is left for the next 24 hours", usd(left), usd(p.DailyUSDMicros)))
	}
	if !in.AlreadyApproved && in.USDMicros > p.ApprovalUSDMicros {
		if in.Pending >= maxPendingPerWallet {
			return refused("too_many_pending", fmt.Sprintf("%d transfers are already waiting for approval", in.Pending))
		}
		return verdict{Outcome: "needs_approval", Detail: fmt.Sprintf("%s is above the %s approval threshold", usd(in.USDMicros), usd(p.ApprovalUSDMicros))}
	}
	return verdict{Outcome: "execute"}
}
