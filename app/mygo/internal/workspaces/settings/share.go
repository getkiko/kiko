package settings

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/getkiko/kiko/app/mygo/internal/kit"
	"github.com/getkiko/kiko/app/mygo/internal/profiles"
)

// shareOrigin is the Config Share Address row's state: the field's text while it is
// edited, what was wrong with it as committed, and a focus Open asked for.
type shareOrigin struct {
	draft   string
	editing bool
	err     string
	focus   bool
}

// reset drops the edit: the field shows the saved address again.
func (s *shareOrigin) reset() {
	*s = shareOrigin{focus: s.focus}
}

// savedShareOrigin is the share address of the app's preferences.
func (w *Workspace) savedShareOrigin() string {
	return profiles.ShareOrigin(w.env.AppPrefs)
}

// saveShareOrigin stores origin as the share address of every window.
func (w *Workspace) saveShareOrigin(origin string) {
	w.env.AppPrefs.Save(profiles.PrefShareOrigin, origin)
	if w.env.AppPrefsChanged != nil {
		w.env.AppPrefsChanged()
	}
}

// shareOriginRow is Settings › Backup & Sharing › Config Share Address: the
// address of the user's own share service, which the overview shares
// configurations through; empty, sharing is unavailable. Typing only edits
// the field; Enter or leaving it commits: a
// valid address saves normalized, an empty one clears the address, a
// wrong one saves nothing and shows its problem. Escape gives the field
// the saved address back.
func (w *Workspace) shareOriginRow(c *ui.Context) {
	s := &w.share
	saved := w.savedShareOrigin()
	if !s.editing {
		s.draft = saved
	}
	kit.SettingsRow(c, "配置分享地址", "", func() {
		ui.Column(c.Key("share-origin-field")).Width(300).Gap(4).Children(func() {
			input := kit.Input(c, "share-origin", &s.draft, "https://share.example.com").Label("配置分享地址")
			if s.focus {
				s.focus = false
				input.Focus().ScrollIntoView()
			}
			if input.Changed() {
				s.editing, s.err = true, ""
			}
			switch {
			case s.editing && input.Shortcut(0, ui.KeyEscape):
				s.reset()
				s.draft = saved
			case input.Submitted(), s.editing && s.err == "" && !input.Focused():
				w.commitShareOrigin()
			}
			kit.FormError(c, w.shareOriginError(saved))
		})
	})
}

// commitShareOrigin saves the field's address, or keeps it with what is
// wrong with it.
func (w *Workspace) commitShareOrigin() {
	s := &w.share
	if !s.editing {
		return
	}
	if strings.TrimSpace(s.draft) == "" {
		w.saveShareOrigin("")
		s.editing, s.err = false, ""
		return
	}
	origin, err := profiles.NormalizeShareOrigin(s.draft)
	if err != nil {
		s.err = err.Error()
		return
	}
	w.saveShareOrigin(origin)
	s.editing, s.err = false, ""
}

// shareOriginError is the problem the row shows: the edit's while one is
// pending, else what is wrong with the saved address, "" when it is valid
// or unset.
func (w *Workspace) shareOriginError(saved string) string {
	s := &w.share
	if s.editing || s.err != "" || saved == "" {
		return s.err
	}
	if err := profiles.ValidShareOrigin(saved); err != nil {
		return err.Error()
	}
	return ""
}
