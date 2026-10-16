package netx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/k2so-dev/awgx/internal/sysx"
)

const (
	TableMin    = 51700
	TableMax    = 51796
	EchoTable   = 51797
	TproxyTable = 51798
	ProbeTable  = 51799
	HostTable   = 51800
	MainTable   = 254
	PrefMin     = 4000
	PrefMax     = 4999
	PrefLocal   = 4000
	PrefHost    = 4005
	PrefTproxy  = 4010
	PrefEcho    = 4020
	PrefProbe   = 4050
	PrefTest    = 4099
	PrefPeer    = 4100
	PrefDefault = 4900
	ProbeMark   = 0x5a17
	TproxyMark  = 0x5a18
	EchoMark    = 0x5a19
	RuleProto   = "158"
	RelayBase   = 20000
)

func families() []int {
	if sysx.Exists("/proc/sys/net/ipv6") {
		return []int{4, 6}
	}
	return []int{4}
}

type Rule struct {
	Family int          `json:"family"`
	Pref   int          `json:"pref"`
	From   netip.Prefix `json:"from,omitzero"`
	To     netip.Prefix `json:"to,omitzero"`
	IIf    string       `json:"iif,omitempty"`
	FwMark uint32       `json:"fwmark,omitempty"`
	Table  int          `json:"table"`

	SuppressDefault bool `json:"suppress_default,omitempty"`
}

func (r Rule) selector() []string {
	args := []string{"pref", strconv.Itoa(r.Pref)}
	if r.From.IsValid() {
		args = append(args, "from", r.From.String())
	}
	if r.To.IsValid() {
		args = append(args, "to", r.To.String())
	}
	if r.IIf != "" {
		args = append(args, "iif", r.IIf)
	}
	if r.FwMark != 0 {
		args = append(args, "fwmark", fmt.Sprintf("0x%x", r.FwMark))
	}
	args = append(args, "lookup", tableName(r.Table))
	if r.SuppressDefault {
		args = append(args, "suppress_prefixlength", "0")
	}
	return args
}

func (r Rule) String() string {
	return fmt.Sprintf("rule -%d %s", r.Family, strings.Join(r.selector(), " "))
}

func tableName(t int) string {
	if t == MainTable {
		return "main"
	}
	return strconv.Itoa(t)
}

type Route struct {
	Family int        `json:"family"`
	Table  int        `json:"table"`
	Type   string     `json:"type,omitempty"`
	Dev    string     `json:"dev,omitempty"`
	Via    netip.Addr `json:"via,omitzero"`
	Metric int        `json:"metric,omitempty"`
}

func (r Route) spec() []string {
	var args []string
	if r.Type != "" && r.Type != "unicast" {
		args = append(args, r.Type)
	}
	args = append(args, "default")
	if r.Via.IsValid() {
		args = append(args, "via", r.Via.String())
	}
	if r.Dev != "" {
		args = append(args, "dev", r.Dev)
	}
	args = append(args, "table", strconv.Itoa(r.Table))
	if r.Metric != 0 {
		args = append(args, "metric", strconv.Itoa(r.Metric))
	}
	return args
}

func (r Route) String() string {
	return fmt.Sprintf("route -%d %s", r.Family, strings.Join(r.spec(), " "))
}

type Policy struct {
	Rules  []Rule
	Routes []Route
}

func (p Policy) Lines() []string {
	var out []string
	for _, r := range p.Routes {
		out = append(out, r.String())
	}
	for _, r := range p.Rules {
		out = append(out, r.String())
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func Diff(from, to []string) []string {
	var out []string
	for _, l := range from {
		if !slices.Contains(to, l) {
			out = append(out, "- "+l)
		}
	}
	for _, l := range to {
		if !slices.Contains(from, l) {
			out = append(out, "+ "+l)
		}
	}
	return out
}

func Drift(have, want Policy) []string {
	fams := families()
	var h, w Policy
	for _, r := range have.Routes {
		if r.Table != ProbeTable {
			h.Routes = append(h.Routes, r)
		}
	}
	for _, r := range have.Rules {
		if r.Pref != PrefProbe && r.Pref != PrefTest {
			h.Rules = append(h.Rules, r)
		}
	}
	for _, r := range want.Routes {
		if slices.Contains(fams, r.Family) {
			w.Routes = append(w.Routes, r)
		}
	}
	for _, r := range want.Rules {
		if slices.Contains(fams, r.Family) {
			w.Rules = append(w.Rules, r)
		}
	}
	return Diff(h.Lines(), w.Lines())
}

type liveRule struct {
	Priority int    `json:"priority"`
	Src      string `json:"src"`
	SrcLen   *int   `json:"srclen"`
	Dst      string `json:"dst"`
	DstLen   *int   `json:"dstlen"`
	IIf      string `json:"iif"`
	FwMark   string `json:"fwmark"`
	Table    string `json:"table"`
	Suppress *int   `json:"suppress_prefixlen"`
	Protocol string `json:"protocol"`
}

func (l liveRule) ours() bool {
	if l.Protocol != "" {
		return l.Protocol == RuleProto
	}
	t := parseTable(l.Table)
	return t >= TableMin && t <= ProbeTable || t == MainTable && l.IIf != ""
}

func prefix(addr string, n *int) netip.Prefix {
	if addr == "" || addr == "all" {
		return netip.Prefix{}
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return netip.Prefix{}
	}
	bits := a.BitLen()
	if n != nil {
		bits = *n
	}
	return netip.PrefixFrom(a, bits)
}

func parseTable(s string) int {
	switch s {
	case "main":
		return MainTable
	case "local":
		return 255
	case "default":
		return 253
	}
	n, _ := strconv.Atoi(s)
	return n
}

func ReadRules(ctx context.Context, run sysx.Runner, min, max int) ([]Rule, error) {
	var out []Rule
	for _, family := range families() {
		b, err := run.Run(ctx, "ip", "-j", "-"+strconv.Itoa(family), "rule", "show")
		if err != nil {
			return nil, err
		}
		var raw []liveRule
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		for _, l := range raw {
			if l.Priority < min || l.Priority > max || !l.ours() {
				continue
			}
			r := Rule{Family: family, Pref: l.Priority, From: prefix(l.Src, l.SrcLen), To: prefix(l.Dst, l.DstLen),
				IIf: l.IIf, Table: parseTable(l.Table), SuppressDefault: l.Suppress != nil && *l.Suppress == 0}
			if l.FwMark != "" {
				v, _ := strconv.ParseUint(strings.TrimPrefix(l.FwMark, "0x"), 16, 32)
				r.FwMark = uint32(v)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

type liveRoute struct {
	Type    string `json:"type"`
	Dst     string `json:"dst"`
	Gateway string `json:"gateway"`
	Dev     string `json:"dev"`
	Table   string `json:"table"`
	Metric  int    `json:"metric"`
}

func ReadRoutes(ctx context.Context, run sysx.Runner, min, max int) ([]Route, error) {
	var out []Route
	for _, family := range families() {
		b, err := run.Run(ctx, "ip", "-j", "-"+strconv.Itoa(family), "route", "show", "table", "all")
		if err != nil {
			return nil, err
		}
		var raw []liveRoute
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		for _, l := range raw {
			t := parseTable(l.Table)
			if t < min || t > max || l.Dst != "default" {
				continue
			}
			r := Route{Family: family, Table: t, Dev: l.Dev, Metric: l.Metric}
			if l.Type != "" && l.Type != "unicast" {
				r.Type = l.Type
				if l.Type != "local" {
					r.Dev = ""
				}
			}
			r.Via, _ = netip.ParseAddr(l.Gateway)
			if family == 6 && r.Metric == 1024 {
				r.Metric = 0
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func ReadPolicy(ctx context.Context, run sysx.Runner) (Policy, error) {
	rules, err := ReadRules(ctx, run, PrefMin, PrefMax)
	if err != nil {
		return Policy{}, err
	}
	routes, err := ReadRoutes(ctx, run, TableMin, ProbeTable)
	if err != nil {
		return Policy{}, err
	}
	return Policy{Rules: rules, Routes: routes}, nil
}

func AddRule(ctx context.Context, run sysx.Runner, r Rule) error {
	_, err := run.Run(ctx, "ip", append([]string{"-" + strconv.Itoa(r.Family), "rule", "add"}, append(r.selector(), "protocol", RuleProto)...)...)
	return ignore(err, "File exists")
}

func DelRule(ctx context.Context, run sysx.Runner, r Rule) error {
	_, err := run.Run(ctx, "ip", append([]string{"-" + strconv.Itoa(r.Family), "rule", "del"}, r.selector()...)...)
	return ignore(err, "No such file or directory")
}

func ignore(err error, msg string) error {
	if err != nil && strings.Contains(err.Error(), msg) {
		return nil
	}
	return err
}

func AddRoute(ctx context.Context, run sysx.Runner, r Route) error {
	_, err := run.Run(ctx, "ip", append([]string{"-" + strconv.Itoa(r.Family), "route", "replace"}, r.spec()...)...)
	return err
}

func DelRoute(ctx context.Context, run sysx.Runner, r Route) error {
	_, err := run.Run(ctx, "ip", append([]string{"-" + strconv.Itoa(r.Family), "route", "del"}, r.spec()...)...)
	return ignore(err, "No such process")
}

func ApplyPolicy(ctx context.Context, run sysx.Runner, want Policy, log func(string)) error {
	have, err := ReadPolicy(ctx, run)
	if err != nil {
		return err
	}
	fams := families()
	var errs []error
	haveRoutes := lines(have.Routes)
	for _, r := range want.Routes {
		if slices.Contains(fams, r.Family) && !slices.Contains(haveRoutes, r.String()) {
			if err := AddRoute(ctx, run, r); err != nil {
				errs = append(errs, err)
				continue
			}
			log("+ " + r.String())
		}
	}
	haveRules := lines(have.Rules)
	for _, r := range want.Rules {
		if slices.Contains(fams, r.Family) && !slices.Contains(haveRules, r.String()) {
			if err := AddRule(ctx, run, r); err != nil {
				errs = append(errs, err)
				continue
			}
			log("+ " + r.String())
		}
	}
	wantRules := lines(want.Rules)
	for _, r := range have.Rules {
		if r.Pref == PrefProbe || r.Pref == PrefTest {
			continue
		}
		if !slices.Contains(wantRules, r.String()) {
			if err := DelRule(ctx, run, r); err != nil {
				errs = append(errs, err)
				continue
			}
			log("- " + r.String())
		}
	}
	if have, err = ReadPolicy(ctx, run); err != nil {
		return errors.Join(append(errs, err)...)
	}
	wantRoutes := lines(want.Routes)
	for _, r := range have.Routes {
		if r.Table == ProbeTable {
			continue
		}
		if !slices.Contains(wantRoutes, r.String()) {
			if err := DelRoute(ctx, run, r); err != nil {
				errs = append(errs, err)
				continue
			}
			log("- " + r.String())
		}
	}
	return errors.Join(errs...)
}

func lines[T fmt.Stringer](items []T) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.String()
	}
	return out
}

func FlushPolicy(ctx context.Context, run sysx.Runner) error {
	have, err := ReadPolicy(ctx, run)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range have.Rules {
		errs = append(errs, DelRule(ctx, run, r))
	}
	for _, r := range have.Routes {
		errs = append(errs, DelRoute(ctx, run, r))
	}
	return errors.Join(errs...)
}

func ClearTest(ctx context.Context, run sysx.Runner) {
	rules, err := ReadRules(ctx, run, PrefTest, PrefTest)
	if err != nil {
		return
	}
	for _, r := range rules {
		DelRule(ctx, run, r)
	}
}

func ClearProbe(ctx context.Context, run sysx.Runner) {
	have, err := ReadPolicy(ctx, run)
	if err != nil {
		return
	}
	for _, r := range have.Rules {
		if r.Pref == PrefProbe {
			DelRule(ctx, run, r)
		}
	}
	for _, r := range have.Routes {
		if r.Table == ProbeTable && r.Type != "unreachable" {
			DelRoute(ctx, run, r)
		}
	}
}
