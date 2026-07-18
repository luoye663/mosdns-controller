// Package rules owns complete snapshot generation. It never applies runtime patches.
package rules

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/managed-dns/controller/internal/mosdnsclient"
	"github.com/managed-dns/controller/internal/storage"
	"golang.org/x/net/idna"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	statusPending    = "pending"
	statusUnknown    = "unknown"
	statusActive     = "active"
	statusSuperseded = "superseded"
	statusFailed     = "failed"
)

type Rule struct {
	ID          int64  `json:"id"`
	Category    string `json:"category"`
	Action      string `json:"action"`
	MatchType   string `json:"match_type"`
	Pattern     string `json:"pattern"`
	Priority    int    `json:"priority"`
	Source      string `json:"source"`
	Comment     string `json:"comment"`
	Enabled     bool   `json:"enabled"`
	CreatedAtMS int64  `json:"created_at_ms"`
	UpdatedAtMS int64  `json:"updated_at_ms"`
}
type Version struct {
	Version     uint64 `json:"version"`
	Checksum    string `json:"checksum"`
	Status      string `json:"status"`
	RuleCount   int    `json:"rule_count"`
	CreatedAtMS int64  `json:"created_at_ms"`
	ErrorCode   string `json:"error_code,omitempty"`
}
type Service struct {
	store  *storage.Store
	mosdns mosdnsclient.Client
	mu     sync.Mutex
}

func New(store *storage.Store, client mosdnsclient.Client) *Service {
	return &Service{store: store, mosdns: client}
}
func (s *Service) List(ctx context.Context) ([]Rule, error) {
	return s.list(ctx, ` WHERE source NOT LIKE 'subscription:%'`)
}
func (s *Service) allRules(ctx context.Context) ([]Rule, error) {
	return s.list(ctx, ` WHERE source NOT LIKE 'subscription:%'`)
}
func (s *Service) list(ctx context.Context, filter string) ([]Rule, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT id,category,action,match_type,pattern,priority,source,comment,enabled,created_at_ms,updated_at_ms FROM domain_rules`+filter+` ORDER BY category,match_type,normalized_pattern,priority DESC,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 列表 API 必须序列化为空数组而非 null，避免客户端无法按数组渲染空状态。
	out := make([]Rule, 0)
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.ID, &r.Category, &r.Action, &r.MatchType, &r.Pattern, &r.Priority, &r.Source, &r.Comment, &r.Enabled, &r.CreatedAtMS, &r.UpdatedAtMS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Service) Create(ctx context.Context, r Rule, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isSubscriptionRule(r) {
		return Version{}, errors.New("subscription rules must be managed through their source")
	}
	rules, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	id, err := s.nextRuleID(ctx)
	if err != nil {
		return Version{}, err
	}
	r.ID = id
	r.CreatedAtMS = time.Now().UnixMilli()
	r.UpdatedAtMS = r.CreatedAtMS
	if r.Source == "" {
		r.Source = "manual"
	}
	rules = append(rules, r)
	return s.publish(ctx, rules, adminID, requestID, ip, 0)
}
func (s *Service) Update(ctx context.Context, id int64, patch Rule, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	found := false
	for i := range rules {
		if rules[i].ID == id {
			if isSubscriptionRule(rules[i]) {
				return Version{}, errors.New("subscription rules must be managed through their source")
			}
			patch.ID = id
			patch.CreatedAtMS = rules[i].CreatedAtMS
			patch.UpdatedAtMS = time.Now().UnixMilli()
			if patch.Source == "" {
				patch.Source = rules[i].Source
			}
			rules[i] = patch
			found = true
		}
	}
	if !found {
		return Version{}, sql.ErrNoRows
	}
	return s.publish(ctx, rules, adminID, requestID, ip, 0)
}
func (s *Service) Delete(ctx context.Context, id int64, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rules, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	out := rules[:0]
	found := false
	for _, r := range rules {
		if r.ID == id {
			if isSubscriptionRule(r) {
				return Version{}, errors.New("subscription rules must be managed through their source")
			}
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return Version{}, sql.ErrNoRows
	}
	return s.publish(ctx, out, adminID, requestID, ip, 0)
}
func (s *Service) Rollback(ctx context.Context, from uint64, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snapshot []byte
	if err := s.store.DB().QueryRowContext(ctx, `SELECT snapshot_json FROM rule_versions WHERE version=?`, from).Scan(&snapshot); err != nil {
		return Version{}, err
	}
	var prior mosdnsclient.Snapshot
	if err := json.Unmarshal(snapshot, &prior); err != nil {
		return Version{}, err
	}
	rules := make([]Rule, len(prior.Rules))
	for i, r := range prior.Rules {
		rules[i] = fromWire(r, true)
	}
	return s.publish(ctx, rules, adminID, requestID, ip, from)
}
func (s *Service) Versions(ctx context.Context) ([]Version, error) {
	rows, err := s.store.DB().QueryContext(ctx, `SELECT version,checksum,status,rule_count,created_at_ms,COALESCE(error_code,'') FROM rule_versions ORDER BY version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 与规则列表保持一致：没有发布历史时返回 []，不返回 JSON null。
	out := make([]Version, 0)
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.Version, &v.Checksum, &v.Status, &v.RuleCount, &v.CreatedAtMS, &v.ErrorCode); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Service) Test(ctx context.Context, qname string) (any, error) {
	return s.mosdns.Match(ctx, qname)
}
func (s *Service) Version(ctx context.Context, version uint64) (mosdnsclient.Snapshot, error) {
	var raw []byte
	if err := s.store.DB().QueryRowContext(ctx, `SELECT snapshot_json FROM rule_versions WHERE version=?`, version).Scan(&raw); err != nil {
		return mosdnsclient.Snapshot{}, err
	}
	var value mosdnsclient.Snapshot
	return value, json.Unmarshal(raw, &value)
}
func (s *Service) Batch(ctx context.Context, operation string, ids []int64, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) == 0 {
		return Version{}, errors.New("ids must not be empty")
	}
	rules, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	selected := map[int64]bool{}
	for _, id := range ids {
		selected[id] = true
	}
	found := 0
	out := rules[:0]
	for _, r := range rules {
		if !selected[r.ID] {
			out = append(out, r)
			continue
		}
		if isSubscriptionRule(r) {
			return Version{}, errors.New("subscription rules must be managed through their source")
		}
		found++
		switch operation {
		case "enable":
			r.Enabled = true
			out = append(out, r)
		case "disable":
			r.Enabled = false
			out = append(out, r)
		case "delete":
		default:
			return Version{}, errors.New("unsupported batch operation")
		}
	}
	if found != len(selected) {
		return Version{}, sql.ErrNoRows
	}
	return s.publish(ctx, out, adminID, requestID, ip, 0)
}
func (s *Service) Import(ctx context.Context, incoming []Rule, adminID int64, requestID, ip string) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.allRules(ctx)
	if err != nil {
		return Version{}, err
	}
	for i := range incoming {
		if isSubscriptionRule(incoming[i]) {
			return Version{}, errors.New("subscription rules must be managed through their source")
		}
		if incoming[i].ID == 0 {
			id, err := s.nextRuleID(ctx)
			if err != nil {
				return Version{}, err
			}
			incoming[i].ID = id
		}
		now := time.Now().UnixMilli()
		incoming[i].CreatedAtMS = now
		incoming[i].UpdatedAtMS = now
		if incoming[i].Source == "" {
			incoming[i].Source = "import"
		}
		current = append(current, incoming[i])
	}
	return s.publish(ctx, current, adminID, requestID, ip, 0)
}

// Reconcile 只根据运行时 status 收敛 PENDING/UNKNOWN，绝不覆盖未知的更高版本。
func (s *Service) Reconcile(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime, err := s.mosdns.Status(ctx)
	if err != nil {
		return "", err
	}
	var version uint64
	var checksum, status string
	err = s.store.DB().QueryRowContext(ctx, `SELECT version,checksum,status FROM rule_versions WHERE status IN ('pending','unknown') AND version=?`, runtime.SnapshotVersion).Scan(&version, &checksum, &status)
	if err == nil && checksum == runtime.Checksum {
		return "matched_candidate", s.finalize(ctx, version)
	}
	var active uint64
	var activeChecksum string
	err = s.store.DB().QueryRowContext(ctx, `SELECT version,checksum FROM rule_versions WHERE status='active'`).Scan(&active, &activeChecksum)
	if errors.Is(err, sql.ErrNoRows) && runtime.SnapshotVersion == 0 {
		return "empty", nil
	}
	if runtime.SnapshotVersion > active {
		return "degraded", fmt.Errorf("runtime version %d is not a known candidate", runtime.SnapshotVersion)
	}
	if runtime.SnapshotVersion < active {
		// 运行时回退或丢失快照时，以 controller ACTIVE 内容创建新版本重发，版本绝不倒退。
		current, err := s.allRules(ctx)
		if err != nil {
			return "", err
		}
		_, err = s.publish(ctx, current, 0, "reconcile", "", 0)
		return "republished", err
	}
	if runtime.Checksum != activeChecksum {
		return "degraded", errors.New("runtime checksum does not match active version")
	}
	return "unchanged", nil
}
func (s *Service) publish(ctx context.Context, rules []Rule, adminID int64, requestID, ip string, rollbackFrom uint64) (Version, error) {
	existing, _ := s.allRules(ctx)
	routeChanged := routeFingerprint(existing) != routeFingerprint(rules)
	if err := validateRules(rules); err != nil {
		return Version{}, err
	}
	current, err := s.activeVersion(ctx)
	if err != nil {
		return Version{}, err
	}
	version, err := s.nextVersion(ctx)
	if err != nil {
		return Version{}, err
	}
	sets, err := s.subscriptionSets(ctx)
	if err != nil {
		return Version{}, err
	}
	snapshot := buildSnapshot(version, current, rules, sets)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return Version{}, err
	}
	v := Version{Version: version, Checksum: snapshot.Checksum, Status: statusPending, RuleCount: snapshotRuleCount(snapshot), CreatedAtMS: time.Now().UnixMilli()}
	if _, err = s.store.DB().ExecContext(ctx, `INSERT INTO rule_versions(version,schema_version,checksum,status,previous_version,rollback_from_version,rule_count,regexp_rule_count,snapshot_json,created_by,created_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, version, 2, snapshot.Checksum, statusPending, current, nullableVersion(rollbackFrom), v.RuleCount, regexpCount(snapshot.Rules), encoded, adminID, v.CreatedAtMS); err != nil {
		return Version{}, err
	}
	validation, err := s.mosdns.Validate(ctx, snapshot)
	if err == nil && !validation.Valid {
		err = errors.New("mosdns rejected snapshot")
	}
	if err != nil {
		return v, s.fail(ctx, version, "VALIDATION_FAILED", err)
	}
	if validation.Checksum != snapshot.Checksum {
		return v, s.fail(ctx, version, "CHECKSUM_MISMATCH", errors.New("mosdns returned a different checksum"))
	}
	applied, err := s.mosdns.Apply(ctx, snapshot)
	if errors.Is(err, mosdnsclient.ErrUnknown) {
		_, _ = s.store.DB().ExecContext(ctx, `UPDATE rule_versions SET status='unknown',error_code='PUBLISH_UNKNOWN' WHERE version=?`, version)
		v.Status = statusUnknown
		// 超时后立即查询运行时状态；只有明确仍为旧版本才能最终判定失败。
		runtime, statusErr := s.mosdns.Status(ctx)
		if statusErr == nil && runtime.SnapshotVersion == version && runtime.Checksum == snapshot.Checksum {
			if finalizeErr := s.finalizeWithRules(ctx, version, rules, adminID, requestID, ip); finalizeErr != nil {
				return v, finalizeErr
			}
			v.Status = statusActive
			return v, nil
		}
		if statusErr == nil && runtime.SnapshotVersion == current {
			return v, s.fail(ctx, version, "APPLY_TIMEOUT_NOT_APPLIED", errors.New("mosdns retained previous snapshot"))
		}
		return v, err
	}
	if err != nil {
		return v, s.fail(ctx, version, "APPLY_FAILED", err)
	}
	if !applied.Applied || applied.Version != version || applied.Checksum != snapshot.Checksum {
		return v, s.fail(ctx, version, "APPLY_RESPONSE_INVALID", errors.New("invalid apply response"))
	}
	if err := s.finalizeWithRules(ctx, version, rules, adminID, requestID, ip); err != nil {
		return v, err
	}
	if routeChanged {
		if err := s.mosdns.Flush(ctx, "cache_local"); err != nil {
			return v, err
		}
		if err := s.mosdns.Flush(ctx, "cache_remote"); err != nil {
			return v, err
		}
	}
	v.Status = statusActive
	return v, nil
}
func (s *Service) finalize(ctx context.Context, version uint64) error {
	var raw []byte
	if err := s.store.DB().QueryRowContext(ctx, `SELECT snapshot_json FROM rule_versions WHERE version=?`, version).Scan(&raw); err != nil {
		return err
	}
	var snap mosdnsclient.Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return err
	}
	rules := make([]Rule, len(snap.Rules))
	for i, r := range snap.Rules {
		rules[i] = fromWire(r, true)
	}
	return s.finalizeWithRules(ctx, version, rules, 0, "reconcile", "")
}
func (s *Service) finalizeWithRules(ctx context.Context, version uint64, rules []Rule, adminID int64, requestID, ip string) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE rule_versions SET status='superseded' WHERE status='active'`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE rule_versions SET status='active',activated_at_ms=?,error_code=NULL,error_message=NULL WHERE version=?`, time.Now().UnixMilli(), version); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM domain_rules`); err != nil {
		return err
	}
	insertRule, err := tx.PrepareContext(ctx, `INSERT INTO domain_rules(id,version,category,action,match_type,pattern,normalized_pattern,priority,source,comment,enabled,created_at_ms,updated_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer insertRule.Close()
	for _, r := range rules {
		normalized, err := normalize(r.Pattern, r.MatchType)
		if err != nil {
			return err
		}
		if _, err = insertRule.ExecContext(ctx, r.ID, version, r.Category, r.Action, r.MatchType, r.Pattern, normalized, r.Priority, r.Source, r.Comment, r.Enabled, r.CreatedAtMS, r.UpdatedAtMS); err != nil {
			return err
		}
	}
	if adminID != 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO admin_audit_logs(admin_id,action,resource_type,request_id,client_ip,result,created_at_ms) VALUES(?,?,?,?,?,?,?)`, adminID, "publish", "rule_version", requestID, ip, "success", time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) fail(ctx context.Context, version uint64, code string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	_, err := s.store.DB().ExecContext(ctx, `UPDATE rule_versions SET status='failed',error_code=?,error_message=? WHERE version=?`, code, message, version)
	if err != nil {
		return err
	}
	return cause
}
func (s *Service) activeVersion(ctx context.Context) (uint64, error) {
	var v uint64
	err := s.store.DB().QueryRowContext(ctx, `SELECT version FROM rule_versions WHERE status='active'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return v, err
}
func (s *Service) nextVersion(ctx context.Context) (uint64, error) {
	var v uint64
	err := s.store.DB().QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0)+1 FROM rule_versions`).Scan(&v)
	return v, err
}
func (s *Service) nextRuleID(ctx context.Context) (int64, error) {
	ids, err := s.reserveRuleIDs(ctx, 1)
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}
func (s *Service) reserveRuleIDs(ctx context.Context, count int) ([]int64, error) {
	if count < 1 {
		return nil, errors.New("rule ID count must be greater than zero")
	}
	_, err := s.store.DB().ExecContext(ctx, `INSERT OR IGNORE INTO system_state(key,value_json,updated_at_ms) VALUES('next_rule_id','0',?)`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	var raw string
	if err = s.store.DB().QueryRowContext(ctx, `SELECT value_json FROM system_state WHERE key='next_rule_id'`).Scan(&raw); err != nil {
		return nil, err
	}
	var first int64
	_, err = fmt.Sscan(raw, &first)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, count)
	for i := range ids {
		first++
		ids[i] = first
	}
	_, err = s.store.DB().ExecContext(ctx, `UPDATE system_state SET value_json=?,updated_at_ms=? WHERE key='next_rule_id'`, fmt.Sprint(first), time.Now().UnixMilli())
	return ids, err
}
func buildSnapshot(version, current uint64, rules []Rule, sets []mosdnsclient.SubscriptionSet) mosdnsclient.Snapshot {
	wire := make([]mosdnsclient.Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled {
			wire = append(wire, toWire(r))
		}
	}
	sort.Slice(wire, func(i, j int) bool {
		a, b := wire[i], wire[j]
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		if a.MatchType != b.MatchType {
			return a.MatchType < b.MatchType
		}
		if a.Pattern != b.Pattern {
			return a.Pattern < b.Pattern
		}
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		return a.ID < b.ID
	})
	snap := mosdnsclient.Snapshot{SchemaVersion: 2, Version: version, ExpectedCurrentVersion: current, GeneratedAt: time.Now().UTC(), BlockRCode: 3, Rules: wire, SubscriptionSets: sets}
	canonical := struct {
		SchemaVersion    uint32                         `json:"schema_version"`
		Version          uint64                         `json:"version"`
		Expected         uint64                         `json:"expected_current_version"`
		GeneratedAt      time.Time                      `json:"generated_at"`
		BlockRCode       int                            `json:"block_rcode"`
		Rules            []mosdnsclient.Rule            `json:"rules"`
		SubscriptionSets []mosdnsclient.SubscriptionSet `json:"subscription_sets,omitempty"`
	}{snap.SchemaVersion, snap.Version, snap.ExpectedCurrentVersion, snap.GeneratedAt, snap.BlockRCode, snap.Rules, snap.SubscriptionSets}
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	snap.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	return snap
}
func snapshotRuleCount(snapshot mosdnsclient.Snapshot) int {
	count := len(snapshot.Rules)
	for _, set := range snapshot.SubscriptionSets {
		count += len(set.Domains)
	}
	return count
}
func toWire(r Rule) mosdnsclient.Rule {
	return mosdnsclient.Rule{ID: r.ID, Category: r.Category, Action: r.Action, MatchType: r.MatchType, Pattern: r.Pattern, Priority: r.Priority, Source: r.Source, Comment: r.Comment}
}
func fromWire(r mosdnsclient.Rule, enabled bool) Rule {
	return Rule{ID: r.ID, Category: r.Category, Action: r.Action, MatchType: r.MatchType, Pattern: r.Pattern, Priority: r.Priority, Source: r.Source, Comment: r.Comment, Enabled: enabled, CreatedAtMS: time.Now().UnixMilli(), UpdatedAtMS: time.Now().UnixMilli()}
}
func regexpCount(rs []mosdnsclient.Rule) int {
	n := 0
	for _, r := range rs {
		if r.MatchType == "regexp" {
			n++
		}
	}
	return n
}
func nullableVersion(v uint64) any {
	if v == 0 {
		return nil
	}
	return v
}
func routeFingerprint(rs []Rule) string {
	var out []string
	for _, r := range rs {
		if r.Category == "route" {
			out = append(out, fmt.Sprintf("%d:%s:%s:%s:%d:%t", r.ID, r.Action, r.MatchType, r.Pattern, r.Priority, r.Enabled))
		}
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}
func isSubscriptionRule(r Rule) bool { return strings.HasPrefix(r.Source, "subscription:") }
func validateRules(rules []Rule) error {
	if len(rules) > 200000 {
		return errors.New("rule limit exceeded")
	}
	seen := map[string]bool{}
	regexps := 0
	for i := range rules {
		r := &rules[i]
		if r.Priority == 0 {
			r.Priority = 100
		}
		if r.Priority < 0 || r.Priority > 1000 {
			return errors.New("priority must be between 0 and 1000")
		}
		if len([]rune(r.Comment)) > 500 {
			return errors.New("comment exceeds 500 characters")
		}
		if !validAction(r.Category, r.Action) {
			return errors.New("invalid category/action")
		}
		n, err := normalize(r.Pattern, r.MatchType)
		if err != nil {
			return err
		}
		r.Pattern = n
		if r.MatchType == "regexp" {
			regexps++
			if len(r.Pattern) > 512 {
				return errors.New("regexp exceeds 512 bytes")
			}
			if _, err := regexp.Compile(r.Pattern); err != nil {
				return err
			}
		}
		key := r.Category + "\x00" + r.MatchType + "\x00" + r.Pattern
		if seen[key] {
			return errors.New("duplicate rule pattern in category")
		}
		seen[key] = true
	}
	if regexps > 500 {
		return errors.New("regexp rule limit exceeded")
	}
	return nil
}
func validAction(category, action string) bool {
	return (category == "access" && (action == "allow" || action == "block")) || (category == "route" && (action == "local" || action == "remote")) || (category == "logging" && action == "no_log")
}
func normalize(pattern, matchType string) (string, error) {
	p := strings.TrimSpace(pattern)
	if matchType == "regexp" {
		if p == "" {
			return "", errors.New("regexp is empty")
		}
		return p, nil
	}
	if matchType != "full" && matchType != "domain" {
		return "", errors.New("invalid match type")
	}
	p = strings.TrimSuffix(strings.ToLower(p), ".")
	if p == "" || strings.Contains(p, "*") {
		return "", errors.New("invalid domain pattern")
	}
	ascii, err := idna.Lookup.ToASCII(p)
	if err != nil {
		return "", err
	}
	if len(ascii) > 253 || !utf8.ValidString(ascii) {
		return "", errors.New("invalid domain length")
	}
	for _, l := range strings.Split(ascii, ".") {
		if l == "" || len(l) > 63 {
			return "", errors.New("invalid domain label")
		}
	}
	return strings.ToLower(ascii), nil
}
