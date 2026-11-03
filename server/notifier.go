package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/notify"
)

// Global settings keys (stored in the settings key-value table; no dedicated table needed).
const (
	// settingNotifyEnabled is the master switch for notifications. Default on: it is a
	// one-click kill switch for maintenance windows, not the feature's enable condition — the real condition is "whether a channel is configured".
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the externally reachable address used to build
	// finding detail back-links (e.g. https://uroboros.example.com). Leave empty and messages carry no back-link button.
	// The project has no reusable external-address setting, so this adds one.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the digest-mode interval (minutes).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the delivery engine's poll interval. 3s is the engine's real-time ceiling,
	// and the main source of latency between "finding persisted" and "message reaches the IM".
	notifyTick = 3 * time.Second
	// notifyLease is the lease duration held when claiming a delivery. It must be well above the
	// worst-case cost of a single delivery (the notify package's HTTP client times out at 15s),
	// or the same row gets delivered by two dispatchers at once.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick caps how many events are fanned out per tick, so enabling a channel for
	// the first time does not expand the entire historical backlog into delivery tasks at once.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest interval.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the per-tick delivery cap for channels with no rate limit.
	// It exists to stop "a channel set to unlimited + a scan that turns up thousands of findings"
	// from dragging a single loop iteration into a long block.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is how many deliveries a single channel sends per tick.
	//
	// This cap is derived from the **lease duration**: claiming a row stamps it with a lease (notifyLease = 3 minutes).
	// If a tick delivers serially long enough for the worst case to exceed the lease, the later rows expire before they are sent.
	// Within one process this is harmless (Run is a single goroutine, serial; ticks never re-enter), but with **two
	// processes on the same DB** the peer re-claims the lease-expired rows and sends them again, also
	// double-incrementing attempts and judging them failed while the original process is still delivering.
	//
	// Value: 3-minute lease / 30s per-send timeout = 6 would use the **whole lease** with zero margin,
	// so it is off the table; 5 keeps the worst case at 150s, leaving 30s of margin. This relationship is
	// pinned by TestNotifyTickBudgetFitsWithinLease — changing notifyLease,
	// notifySendTimeout, or this value will break that assertion.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the timeout for a single delivery. It also determines the previous constant's value;
	// their product must not exceed notifyLease, see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the backoff sequence for failed retries, indexed by attempts already made.
// 3 chances (including the first) correspond to db.MaxNotifyAttempts; the two must change together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier is the delivery engine for finding notifications.
//
// It runs as an independent goroutine alongside Scheduler (see server.New). Reusing
// Scheduler's tick is deliberately avoided: notifications' real-time requirement (3s) differs
// from triggers' business cadence, and their failures are unrelated — a stuck notification must not affect agent triggers.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu guards buckets. Channels are few and contention is low, so a single mutex is enough;
	// a finer-grained structure is not worth introducing here.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a single channel's token bucket.
//
// A token bucket is used instead of a "count per minute then reset" sliding window because the
// latter has nasty boundary effects: sending 20 at the end of a window and 20 the next instant is
// 40 in one second to the platform, which gets rate-limited; a token bucket refills at a constant rate and avoids such bursts.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx ends. Started once by server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step runs one round: first fan out new events, then deliver the due tasks.
//
// A failure in either step is only logged, never breaking the loop — a notification-system failure must never escalate into a process-level problem.
// Each tick is independent; the next round retries naturally.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] fan out events failed: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] read channels failed: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The token bucket's unit is **number of messages** (equivalent to HTTP requests), not findings.
		// In realtime mode the two are the same (one finding, one message); in digest mode a whole batch
		// of findings becomes one message, so it consumes only one token.
		//
		// Both modes ask the token bucket first, then claim by quota — the order must not be reversed, or a
		// delivery blocked by rate limiting has already burned a retry.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns a digest channel's token consumption and batch-size upper bound for this tick.
//
// The two return values are **two different dimensions**, which is exactly why this is its own function:
//
//   - tokens is the message count. A batch of findings becomes one message and one HTTP request, so it is always 1.
//     rate_per_min therefore still applies to digest (at most this many digest messages per minute).
//   - claimLimit is how many findings this batch holds at most. It is bounded only by memory, unrelated to the request budget.
//
// Once, to make rate_per_min apply to digest, the per-tick request budget
// (notifyMaxSendsPerChannelPerTick, derived from the lease) was passed straight down as the batch size.
// The result: a rate_per_min=20 channel refills only 1 token in a 3s tick, so each digest
// message held only 1 finding — digest degenerated into "realtime pushes with digest wording", and readers got a stream of
// "1 new finding in the last 30 minutes", while db.MaxDigestBatchSize was never reachable.
//
// This symptom is hard to catch in end-to-end tests (existing cases all pass a large enough limit to
// stepDigest by hand, bypassing step's quota math), so the decision is kept here, pinned directly by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and delivers a channel's realtime tasks, one finding per message.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] claim realtime deliveries failed channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel kind %q is not registered", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// A render failure is a local-data problem; retrying will not help.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates a channel's pending deliveries into one message and sends it when the batch is due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] check digest batch failed channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] claim digest batch failed channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel kind %q is not registered", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Deliveries whose snapshot is broken and did not make it into the message must be explicitly failed. Otherwise they stay
	// outside included — in neither the message nor the failure list — and on a successful send their state is
	// missed by the later batch marking, stuck in sending forever until the lease expires and they are re-claimed repeatedly.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "event snapshot could not be parsed; this finding cannot be rendered into a message"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] mark bad-snapshot deliveries failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] skipped %d deliveries with unparseable snapshots channel=%d", len(skipped), ch.ID)
	}
	// Hand send only those that made it into the message: included[i] corresponds strictly to msg.Items[i],
	// and send relies on that correspondence to map "the channel reported it fit the first K" onto the right delivery rows.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and transitions state according to the result.
//
// One batch of deliveries (possibly dozens in digest mode) shares one send result: either delivered, or the whole batch retries.
// No per-item retry — a digest message is a single message, and resending part of it would corrupt the batch semantics.
//
// The only exception is **segmentation caused by the channel length limit**: the channel reports it actually fit only the first K,
// so from K+1 on they must be held for the next batch rather than marked successful along with the rest. Otherwise the truncated
// findings are in neither the message nor the failure list, and vanish entirely.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Cap each delivery so a stuck channel does not drag down the remaining channels this round.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The channel cannot report more delivered than were sent; if it does, the render layer miscounted.
			// Treating it as all-delivered and logging the problem beats corrupting the records.
			log.Printf("[notify] channel reported delivered count %d exceeds delivery count %d channel=%s, treating as all delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] mark delivered failed channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message hit the channel length limit: the rest go back to the queue immediately, continued on the next tick.
			// Use DeferDeliveries rather than RescheduleDeliveries — this is not a failure,
			// and must not consume the retry budget (claiming already optimistically +1'd it; this reverses that).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("message hit the channel length limit; only the first %d delivered, the rest held for the next batch", delivered)); err != nil {
				log.Printf("[notify] queue segmented continuation failed channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither errored nor reported how many it delivered. Treat as failure (with backoff),
		// so this delivery is not re-claimed repeatedly yet never marked.
		err = fmt.Errorf("channel did not report delivered count (delivered=%d)", delivered)
	}

	// Failure disposition is decided **per item**, not by the batch's maximum attempt count.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts`, killing the whole batch, but the
	// attempt counts within a batch differ: one old delivery already retried twice (attempts=2) would drag
	// a brand-new delivery (attempts=1) in the same batch into failed — the new finding is lost permanently without using a single retry,
	// exactly the opposite of the intent "don't let old rows pull new rows under".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] mark failed state errored channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("still failing after %d retries: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] mark failed state errored channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Reschedule grouped by delay: there are only 3 backoff tiers, so the group count is naturally tiny — no need for one
	// UPDATE per item (which would turn a 500-item batch into 500 round-trips).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] reschedule deliveries failed channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] delivery failed channel=%d kind=%s permanent=%d retries-exhausted=%d pending-retry=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns those in all that are not in keep (compared by pointer identity).
// Used to find deliveries that "did not make it into the message" — they must be handled explicitly, not left in limbo.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt fetches the channel implementation and parses its config.
// Returns ok=false when the kind is not registered, so the delivery should be failed outright rather than retried forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On config-parse failure give an empty map: the channel's own Validate reports "which field is missing",
		// and that error guides the user to a fix better than a JSON parse error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single finding message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch renders a digest message. It parses snapshots one by one — a single bad one is just skipped,
// not allowed to sink the whole digest.
//
// The return value included corresponds **strictly one-to-one** with msg.Items (the i-th delivery ↔ the i-th item).
// This correspondence is a hard requirement: the caller uses "the channel reported it fit the first K" to decide which first-K
// deliveries to mark delivered. If a bad snapshot is skipped here without removing the skipped delivery from included,
// the indices shift — a bad item that should fail gets marked delivered, and a good item is misjudged as undelivered.
// The bad ones are explicitly failed by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// A bad snapshot enters neither the message nor included — its disposition is the caller's responsibility
			// (explicitly failed, not slipped through hidden among the "delivered").
			log.Printf("[notify] skipped unparseable snapshot in digest batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("all %d deliveries in the digest batch are unparseable", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders an event snapshot into a pushable item, also resolving asset names and the detail back-link.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// An asset-name lookup failure must not block the push: missing a name is far lighter than missing the notification —
		// the message is just one asset line shorter.
		log.Printf("[notify] resolve asset names failed finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// The detail-page route is web/src/app/(main)/function/findings/detail/page.tsx,
		// which reads the finding id from the query parameter id.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens removes **at most want** tokens from the channel's token bucket and returns how many were actually taken.
//
// One token = one message (one HTTP request). In realtime mode the caller passes as many as it wants;
// in digest mode a whole batch of findings sends one message, so pass 1.
//
// The bucket capacity is the channel's per-minute limit, refilled at a constant rate. ratePerMin<=0 means no rate limit,
// returning a finite but large-enough value so a single loop iteration is not dragged down by an unbounded backlog.
//
// The want cap is required: without it the bucket could only be fully drained, yet the caller has its own per-tick cap, so
// extra tokens are unusable and vanish before the next refill — the accumulated burst capacity becomes permanently unreachable,
// and even "no pending deliveries this round" would still be charged.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Refill by real elapsed time, at a rate of ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before truncating: the token count is accumulated in floating point, so refilling in two steps
	// 0.5 + 0.5 may give 0.9999999999, and a plain int() truncates it to 0 —
	// a mathematically full bucket from which no token can be taken. 1e-9 is far below one token, so it never masks a real shortfall.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the master switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the external back-link address, with trailing slashes removed.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the digest interval, falling back to the default when invalid or unset.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses the snapshot of the event tied to a delivery.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("event snapshot for delivery %d is empty", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("parse event snapshot for delivery %d failed: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// The event kind follows the event row; the one in the snapshot may have been written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
