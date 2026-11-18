package routes

import (
	"context"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/api"
	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/mitmpolicy"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// refreshTrust reads whether the root certificate is trusted; the tests
// judge decryption by it. A failed read keeps the last answer.
func (w *Workspace) refreshTrust() {
	if w.trustLoading || w.env.API == nil {
		return
	}
	w.trustLoading = true
	env.Go(w.env, func(ctx context.Context) (api.MITMStatus, error) {
		return w.env.API.MITMStatus(ctx)
	}, func(status api.MITMStatus, err error) {
		w.trustLoading = false
		if err == nil {
			w.untrusted = !status.CA.Trusted && !status.CA.TrustUnknown
		}
	})
}

// mitmOutcome is the decryption verdict of a test connection to host: the
// local evaluation for the source (device and port), replaced by the
// core's answer once it gave one (answered; an empty value then means not
// decrypted). The bool is false when the verdict is not worth showing
// (decryption is off, or no host).
func mitmOutcome(doc *model.Doc, host string, untrusted bool, src mitmpolicy.Source, core string, answered bool) (mitmpolicy.Outcome, bool) {
	host = strings.TrimSpace(host)
	if host == "" && core == "" {
		return mitmpolicy.Outcome{}, false
	}
	local := mitmpolicy.Evaluate(doc, host, !untrusted, src)
	if local.Decision == mitmpolicy.Skipped && local.Reason == mitmpolicy.ReasonFeatureOff {
		return local, false
	}
	o := mitmpolicy.Merge(local, core, answered)
	if o.Decision == mitmpolicy.None && !answered {
		return o, false
	}
	return o, true
}

// mitmText is the verdict in words: its label and, for a skip the document
// cannot name, the core's reason.
func mitmText(o mitmpolicy.Outcome) string {
	text := o.Label()
	if o.Decision == mitmpolicy.Skipped && o.Reason == mitmpolicy.ReasonCoreOther && o.Detail != "" {
		text += " · " + o.Detail
	}
	return text
}

// mitmProperty is the Decryption row of a test result: a lock and the verdict,
// muted when the connection is left alone.
func mitmProperty(c *ui.Context, o mitmpolicy.Outcome) {
	k := kit.T(c)
	kit.PropertyWith(c, "HTTPS 解密", func() {
		tone := k.Success
		if o.Decision != mitmpolicy.Decrypted {
			tone = k.Label3
		}
		kit.Icon(c, kit.SymLock, 13).TextColor(tone)
		ui.Text(c, mitmText(o)).FontSize(kit.FontTable).Selectable().Grow(1).MinWidth(0).Wrap()
	})
}

// dialogMITM is the verdict of a finished dialog test.
func (t *testDialog) mitmOutcome(doc *model.Doc, untrusted bool, r api.PolicyTestResult) (mitmpolicy.Outcome, bool) {
	if t.dns {
		return mitmpolicy.Outcome{}, false
	}
	return mitmOutcome(doc, t.resultHost, untrusted, t.resultSource, r.MITM, true)
}
