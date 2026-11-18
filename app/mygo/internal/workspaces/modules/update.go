package modules

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/env"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// job is one module to convert again: from its address (update) or from
// its kept text with new argument values (parameters).
type job struct {
	id   string
	auto bool
	// raw converts the kept text with arguments, without downloading the
	// module again.
	raw       bool
	arguments map[string]string
	// interval, when set, becomes the module's update interval with the
	// result.
	interval *int
	// done hears of the result on the UI thread.
	done func(Outcome, error)
}

// updater runs the jobs one after another, on the UI thread's state.
type updater struct {
	queue   []job
	queued  map[string]bool
	running map[string]bool
	current bool
}

func (u *updater) init() {
	u.queued, u.running = map[string]bool{}, map[string]bool{}
}

// busy reports whether the module is waiting for or in an update.
func (u *updater) busy(id string) bool { return u.queued[id] || u.running[id] }

// Update converts the module again from its address, as "Update" does.
func (w *Workspace) Update(id string) {
	w.enqueue(job{id: id, done: w.reportManual})
}

// UpdateAll updates every module that has an address.
func (w *Workspace) UpdateAll() {
	for _, m := range modulesOf(w.doc.Doc()) {
		if m.Source.Kind == model.ModuleSourceURL {
			w.enqueue(job{id: m.ID, done: w.reportManual})
		}
	}
}

// CheckDue updates the modules whose interval has passed, quietly: it
// does nothing while the window is hidden, and a failure only lands in
// the module's LastError. It returns how many it queued.
func (w *Workspace) CheckDue() int {
	if !w.env.Visible() || w.doc == nil {
		return 0
	}
	now := w.now()
	n := 0
	for _, m := range modulesOf(w.doc.Doc()) {
		if Due(m, now) && !w.updates.busy(m.ID) {
			w.enqueue(job{id: m.ID, auto: true})
			n++
		}
	}
	return n
}

// Busy reports whether any module is updating or waiting to.
func (w *Workspace) Busy() bool { return len(w.updates.queued) > 0 || len(w.updates.running) > 0 }

// Errors the queue answers a job with before it runs.
var (
	errBusy    = errors.New("模块正在更新")
	errMissing = errors.New("模块已不存在")
	errNoFetch = errors.New("无法下载")
)

func (w *Workspace) reportManual(o Outcome, err error) {
	switch {
	case errors.Is(err, errBusy):
		// The card shows the update that runs.
	case err != nil:
		w.env.Toast("更新失败：" + shorten(err.Error(), 120))
	case o == Awaiting:
		w.env.Toast("有新内容待确认")
	case o == Changed:
		w.env.Toast("已更新")
	default:
		w.env.Toast("已是最新")
	}
}

func (w *Workspace) enqueue(j job) {
	u := &w.updates
	var refused error
	switch {
	case w.fetch == nil:
		refused = errNoFetch
	case find(w.doc.Doc(), j.id) == nil:
		refused = errMissing
	case u.busy(j.id):
		refused = errBusy
	}
	if refused != nil {
		if j.done != nil {
			j.done(Unchanged, refused)
		}
		return
	}
	u.queue = append(u.queue, j)
	u.queued[j.id] = true
	w.pump()
}

// pump starts the next job when none runs. A quiet job waits no longer
// than the window is visible: while hidden the rest are dropped.
func (w *Workspace) pump() {
	u := &w.updates
	for !u.current && len(u.queue) > 0 {
		j := u.queue[0]
		u.queue = u.queue[1:]
		delete(u.queued, j.id)
		if j.auto && !w.env.Visible() {
			continue
		}
		m := find(w.doc.Doc(), j.id)
		if m == nil {
			continue
		}
		w.run(j, m)
	}
}

// run converts one module off the UI thread and records the outcome.
func (w *Workspace) run(j job, m *model.HTTPModule) {
	u := &w.updates
	u.current, u.running[j.id] = true, true
	address, raw, base := m.Source.URL, m.Raw, m.Source.URL
	args := j.arguments
	if !j.raw {
		args = ArgumentValues(m.Arguments)
	}
	fetch, fetchMain := w.fetch, w.fetchMain
	env.Go(w.env, func(ctx context.Context) (*Converted, error) {
		ctx, cancel := context.WithTimeout(ctx, 3*FetchTimeout)
		defer cancel()
		in := Input{BaseURL: base, Arguments: args}
		if j.raw {
			if raw == "" {
				return nil, errors.New("模块没有可转换的原文")
			}
			in.Text = raw
		} else {
			body, err := Download(ctx, fetchMain, address)
			if err != nil {
				return nil, err
			}
			in.Data = body
		}
		conv, err := Convert(ctx, fetch, in, nil)
		if err != nil {
			return nil, err
		}
		if len(conv.Failures) > 0 {
			return nil, failureError(conv.Failures)
		}
		return conv, nil
	}, func(conv *Converted, err error) {
		u.current = false
		delete(u.running, j.id)
		outcome, err := w.record(j, conv, err)
		if j.done != nil {
			j.done(outcome, err)
		}
		w.pump()
	})
}

// failureError names the first failed download and how many failed.
func failureError(list []Failure) error {
	msg := "下载失败：" + list[0].String()
	if len(list) > 1 {
		msg += fmt.Sprintf("（共 %d 个）", len(list))
	}
	return errors.New(msg)
}

// record writes a job's result to the document as it is now: only the
// module's converted fields (or its error) change, so a switch flipped
// while the download ran stays as the user left it. A document that refuses
// the change gives its error back, and the outcome is Unchanged.
func (w *Workspace) record(j job, conv *Converted, err error) (Outcome, error) {
	now := w.now()
	outcome := Unchanged
	update := func(d *model.Doc) {
		m := find(d, j.id)
		if m == nil {
			return
		}
		if err != nil {
			// A failed parameter change leaves the module alone: the
			// dialog shows the error.
			if !j.raw {
				m.LastError = shorten(err.Error(), 300)
				m.LastCheckedAt = now.UnixMilli()
			}
			return
		}
		if j.interval != nil {
			m.UpdateInterval = *j.interval
		}
		outcome = applyUpdate(m, conv.Update, now)
	}
	if uerr := w.doc.Update(update); uerr != nil {
		if err != nil {
			return Unchanged, err
		}
		if j.done == nil {
			w.env.ToastError(uerr)
		}
		return Unchanged, uerr
	}
	return outcome, err
}

// shorten cuts s to n characters.
func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// Now is the workspace's clock.
func (w *Workspace) Now() time.Time { return w.now() }
