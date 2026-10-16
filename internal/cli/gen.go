package cli

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"

	"github.com/k2so-dev/awgx/internal/awgconf"
	"github.com/k2so-dev/awgx/internal/core"
	"github.com/k2so-dev/awgx/internal/gen"
	"github.com/k2so-dev/awgx/internal/i1"
	"github.com/k2so-dev/awgx/internal/state"
	"github.com/k2so-dev/awgx/internal/validate"
	"github.com/spf13/cobra"
)

type i1Flags struct {
	profile     string
	sni         string
	fingerprint string
	custom      string
	noiseMin    int
	noiseMax    int
	name        string
}

func (f *i1Flags) register(cmd *cobra.Command, name string) {
	f.name = name
	cmd.Flags().StringVar(&f.profile, name, "", "I1 profile: none, noise, quic, stun, dns, custom")
	cmd.Flags().StringVar(&f.sni, "sni", "", "SNI for the quic profile")
	cmd.Flags().StringVar(&f.fingerprint, "fingerprint", "", "browser fingerprint for quic: "+strings.Join(i1.Fingerprints(), ", "))
	cmd.Flags().StringVar(&f.custom, "cps", "", "CPS string for the custom profile")
	cmd.Flags().IntVar(&f.noiseMin, "noise-min", 0, "minimum noise size")
	cmd.Flags().IntVar(&f.noiseMax, "noise-max", 0, "maximum noise size")
}

func (f *i1Flags) settings() (*i1.Settings, error) {
	if f.profile == "" {
		if f.sni != "" || f.fingerprint != "" || f.custom != "" || f.noiseMin != 0 || f.noiseMax != 0 {
			return nil, fmt.Errorf("--sni, --fingerprint, --cps, --noise-min and --noise-max need --%s", f.name)
		}
		return nil, nil
	}
	if !i1.Valid(f.profile) {
		return nil, fmt.Errorf("unknown I1 profile %q", f.profile)
	}
	if f.fingerprint != "" && !slices.Contains(i1.Fingerprints(), f.fingerprint) {
		return nil, fmt.Errorf("unknown fingerprint %q: use %s", f.fingerprint, strings.Join(i1.Fingerprints(), ", "))
	}
	s := i1.Settings{
		Profile:     f.profile,
		SNI:         f.sni,
		Fingerprint: f.fingerprint,
		Custom:      f.custom,
		NoiseMin:    f.noiseMin,
		NoiseMax:    f.noiseMax,
	}
	return &s, nil
}

func genCmd(app *core.App) *cobra.Command {
	var preset, importPath string
	var sets []string
	var dryRun, yes, asJSON bool
	var toggles toggleFlags
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Generate or regenerate instance parameters",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if importPath != "" {
				return importConf(out, importPath, asJSON)
			}
			if !app.Installed() {
				if preset == "" {
					preset = "balanced"
				}
				p, err := gen.PresetByName(preset)
				if err != nil {
					return err
				}
				o, err := gen.Obfuscation(p.With(toggles.get()))
				if err != nil {
					return err
				}
				if len(sets) > 0 {
					if o, err = setFields(o, sets, gen.Toggles{}); err != nil {
						return err
					}
					if err := core.CheckCustom(o).Err(); err != nil {
						return err
					}
				}
				if asJSON {
					return printJSON(out, o)
				}
				fmt.Fprint(out, awgconf.Config{Interface: awgconf.Interface{Obfuscation: o}}.String())
				return nil
			}
			cur, err := app.Current()
			if err != nil {
				return err
			}
			var obf awgconf.Obfuscation
			var serverSide bool
			var save func() error
			if len(sets) > 0 {
				if preset != "" {
					return fmt.Errorf("--set and --preset cannot be combined")
				}
				if obf, err = setFields(cur.Obfuscation, sets, toggles.get()); err != nil {
					return err
				}
				next := cur
				next.Obfuscation = obf
				is, err := app.CheckInstance(next)
				printIssues(cmd.ErrOrStderr(), is)
				if err != nil {
					return err
				}
				save = func() error {
					_, _, err := app.SetObfuscation(cmd.Context(), cur.Name, obf)
					return err
				}
			} else {
				next, issues, err := app.Regenerate(cur, preset, toggles.get())
				printIssues(cmd.ErrOrStderr(), issues)
				if err != nil {
					return err
				}
				obf = next.Obfuscation
				save = func() error { return app.SaveRegenerated(cmd.Context(), next) }
			}
			store, _ := app.Store()
			peers, err := store.Peers(cur.Name)
			if err != nil {
				return err
			}
			changes := diffObfuscation(cur.Obfuscation, obf)
			serverSide = !cur.Obfuscation.SameServerSide(obf)
			summary := map[string]any{"changes": changes, "server_side": serverSide, "affected_peers": peerNames(peers)}
			info := out
			if asJSON {
				info = cmd.ErrOrStderr()
			}
			for _, c := range changes {
				fmt.Fprintln(info, c)
			}
			switch {
			case len(changes) == 0:
				fmt.Fprintln(info, "no changes")
			case serverSide:
				fmt.Fprintf(info, "server-side parameters change: all %d peers need new configs: %v\n", len(peers), peerNames(peers))
			default:
				fmt.Fprintln(info, "client-side parameters change: old configs keep working, re-export them to use the new values")
			}
			if len(changes) > 0 && !dryRun {
				if !confirm(yes, "apply new parameters to "+cur.Name+"?") {
					return fmt.Errorf("aborted")
				}
				err := app.Locked(func() error {
					now, err := app.Current()
					if err != nil {
						return err
					}
					if !reflect.DeepEqual(now, cur) {
						return fmt.Errorf("%s changed meanwhile, run the command again", cur.Name)
					}
					return save()
				})
				if err != nil {
					return err
				}
			}
			if asJSON {
				return printJSON(out, summary)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&preset, "preset", "", "preset: balanced, stealth, speed (current by default)")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "set one parameter, KEY=VALUE: jc, jmin, jmax, s1-s4, h1-h4, i2-i5 (makes the preset custom)")
	toggles.register(cmd)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the diff and affected clients without applying")
	cmd.Flags().StringVar(&importPath, "import", "", "validate an existing .conf file and list violations")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&asJSON, "json", false, "JSON output")
	return cmd
}

func importConf(out io.Writer, path string, asJSON bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	c, err := awgconf.Parse(f)
	if err != nil {
		return err
	}
	is := validate.Obfuscation(c.Interface.Obfuscation, gen.PathMTU)
	if asJSON {
		if err := printJSON(out, is); err != nil {
			return err
		}
	} else if len(is) == 0 {
		fmt.Fprintln(out, "no violations")
	} else {
		printIssues(out, is)
	}
	if is.Failed() {
		return exitError{2}
	}
	return nil
}

func setFields(o awgconf.Obfuscation, sets []string, t gen.Toggles) (awgconf.Obfuscation, error) {
	f := gen.FieldsOf(o)
	keys := map[string]*string{
		"jc": &f.Jc, "jmin": &f.Jmin, "jmax": &f.Jmax,
		"s1": &f.S1, "s2": &f.S2, "s3": &f.S3, "s4": &f.S4,
		"h1": &f.H1, "h2": &f.H2, "h3": &f.H3, "h4": &f.H4,
		"i2": &f.I2, "i3": &f.I3, "i4": &f.I4, "i5": &f.I5,
	}
	for _, s := range sets {
		k, v, ok := strings.Cut(s, "=")
		dst := keys[strings.ToLower(strings.TrimSpace(k))]
		if !ok || dst == nil {
			return o, fmt.Errorf("--set %q: use KEY=VALUE, KEY is jc, jmin, jmax, s1-s4, h1-h4 or i2-i5", s)
		}
		*dst = v
	}
	if t.RandomTrailers != nil {
		f.Trailers = *t.RandomTrailers
	}
	if t.DisableCookies != nil {
		f.NoCookie = *t.DisableCookies
	}
	return f.Apply(o)
}

func diffObfuscation(a, b awgconf.Obfuscation) []string {
	var out []string
	add := func(name string, x, y any) {
		if fmt.Sprint(x) != fmt.Sprint(y) {
			out = append(out, fmt.Sprintf("%s: %v -> %v", name, x, y))
		}
	}
	add("Jc", a.Jc, b.Jc)
	add("Jmin", a.Jmin, b.Jmin)
	add("Jmax", a.Jmax, b.Jmax)
	add("S1", a.S1, b.S1)
	add("S2", a.S2, b.S2)
	add("S3", a.S3, b.S3)
	add("S4", a.S4, b.S4)
	add("H1", a.H1, b.H1)
	add("H2", a.H2, b.H2)
	add("H3", a.H3, b.H3)
	add("H4", a.H4, b.H4)
	if a.HeaderProtectionKey != b.HeaderProtectionKey {
		out = append(out, "HeaderProtectionKey: changed")
	}
	add("RandomTrailers", a.RandomTrailers, b.RandomTrailers)
	add("DisableCookies", a.DisableCookies, b.DisableCookies)
	add("ContentPaddingAddition", a.ContentPaddingAddition, b.ContentPaddingAddition)
	add("RekeyAfterTime", a.RekeyAfterTime, b.RekeyAfterTime)
	add("RejectAfterTime", a.RejectAfterTime, b.RejectAfterTime)
	for i := range a.I {
		if a.I[i] != b.I[i] {
			out = append(out, fmt.Sprintf("I%d: changed", i+1))
		}
	}
	return out
}

func peerNames(peers []state.Peer) []string {
	names := make([]string, len(peers))
	for i, p := range peers {
		names[i] = p.Name
	}
	return names
}

type toggleFlags struct {
	cmd            *cobra.Command
	randomTrailers bool
	disableCookies bool
}

func (t *toggleFlags) register(cmd *cobra.Command) {
	t.cmd = cmd
	cmd.Flags().BoolVar(&t.randomTrailers, "random-trailers", false, "random trailers on handshakes and data, forces S1=S2=S3=S4 (server-side)")
	cmd.Flags().BoolVar(&t.disableCookies, "disable-cookies", false, "never send cookie replies: removes the S3 message, loses handshake flood protection (server-side)")
}

func (t *toggleFlags) get() gen.Toggles {
	var g gen.Toggles
	if t.cmd.Flags().Changed("random-trailers") {
		v := t.randomTrailers
		g.RandomTrailers = &v
	}
	if t.cmd.Flags().Changed("disable-cookies") {
		v := t.disableCookies
		g.DisableCookies = &v
	}
	return g
}
