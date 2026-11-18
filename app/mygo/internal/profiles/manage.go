package profiles

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/getkiko/kiko/app/mygo/internal/emit"
	"github.com/getkiko/kiko/app/mygo/internal/model"
)

// Summary describes a document in a line: "1 in · 2 out · 3 rules" (in Chinese), with
// subscriptions when it has some.
func Summary(doc *model.Doc) string {
	parts := []string{fmt.Sprintf("%d 入", len(doc.Inbounds)), fmt.Sprintf("%d 出", len(doc.Outbounds))}
	if len(doc.Subscriptions) > 0 {
		parts = append(parts, fmt.Sprintf("%d 订", len(doc.Subscriptions)))
	}
	parts = append(parts, fmt.Sprintf("%d 规则", len(doc.Route.Rules)))
	return strings.Join(parts, " · ")
}

// Sorted returns the profiles, the most recently saved first.
func Sorted(profiles []*model.Profile) []*model.Profile {
	sorted := slices.Clone(profiles)
	slices.SortStableFunc(sorted, func(a, b *model.Profile) int {
		switch {
		case a.SavedAt > b.SavedAt:
			return -1
		case a.SavedAt < b.SavedAt:
			return 1
		}
		return 0
	})
	return sorted
}

// Find returns the profile with id, nil when there is none.
func Find(profiles []*model.Profile, id string) *model.Profile {
	for _, p := range profiles {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func nameTaken(profiles []*model.Profile, name, except string) bool {
	return slices.ContainsFunc(profiles, func(p *model.Profile) bool { return p.Name == name && p.ID != except })
}

// Errors of naming a profile.
var (
	ErrNameRequired = errors.New("请填写配置名称")
	ErrNameTaken    = errors.New("已存在同名配置")
)

// CheckName validates a profile name, trimmed, for the profile except
// ("" for a new one).
func CheckName(profiles []*model.Profile, name, except string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrNameRequired
	}
	if nameTaken(profiles, name, except) {
		return "", ErrNameTaken
	}
	return name, nil
}

// NewProfile is an empty document under a new name.
func NewProfile(profiles []*model.Profile, name, platform string) (*model.Profile, error) {
	name, err := CheckName(profiles, name, "")
	if err != nil {
		return nil, err
	}
	return &model.Profile{ID: model.NewID("prof"), Name: name, SavedAt: time.Now().UnixMilli(), Doc: model.EmptyDoc(platform)}, nil
}

// Duplicate copies p under the first free name "<name> copy", "<name> copy 2"…, in Chinese.
func Duplicate(profiles []*model.Profile, p *model.Profile) *model.Profile {
	name := p.Name + " 副本"
	for i := 2; nameTaken(profiles, name, ""); i++ {
		name = fmt.Sprintf("%s 副本 %d", p.Name, i)
	}
	return &model.Profile{ID: model.NewID("prof"), Name: name, SavedAt: time.Now().UnixMilli(), Doc: model.Clone(p.Doc)}
}

// Renamed returns the profiles with p's name changed.
func Renamed(profiles []*model.Profile, id, name string) []*model.Profile {
	out := make([]*model.Profile, len(profiles))
	for i, p := range profiles {
		out[i] = p
		if p.ID == id {
			copied := *p
			copied.Name = name
			out[i] = &copied
		}
	}
	return out
}

// Deletion is what deleting a profile leaves.
type Deletion struct {
	Profiles []*model.Profile
	// ActiveID is the profile then active; Doc, when set, becomes the
	// document.
	ActiveID string
	Doc      *model.Doc
}

// Delete removes the profile id. Deleting the active profile loads the most
// recently saved of the others, or a new empty profile when none is
// left; a profile whose dependencies are broken cannot be loaded.
func Delete(profiles []*model.Profile, id, activeID, platform string, library []*model.NetworkConfig) (Deletion, error) {
	remaining := slices.DeleteFunc(slices.Clone(profiles), func(p *model.Profile) bool { return p.ID == id })
	if id != activeID {
		return Deletion{Profiles: remaining, ActiveID: activeID}, nil
	}
	if len(remaining) > 0 {
		next := Sorted(remaining)[0]
		if err := LoadError(next, library); err != nil {
			return Deletion{}, err
		}
		return Deletion{Profiles: remaining, ActiveID: next.ID, Doc: model.FillDefaults(model.Clone(next.Doc))}, nil
	}
	fallback := &model.Profile{ID: model.NewID("prof"), Name: "未命名配置", SavedAt: time.Now().UnixMilli(), Doc: model.EmptyDoc(platform)}
	return Deletion{Profiles: []*model.Profile{fallback}, ActiveID: fallback.ID, Doc: model.Clone(fallback.Doc)}, nil
}

// LoadError is why a profile cannot become the document: its first broken
// dependency.
func LoadError(p *model.Profile, library []*model.NetworkConfig) error {
	if problems := emit.Emit(p.Doc, emit.Options{NetworkConfigs: library}).DependencyErrors; len(problems) > 0 {
		return errors.New(problems[0])
	}
	return nil
}

// Import is the outcome of importing profiles.
type Import struct {
	Profiles []*model.Profile
	Library  []*model.NetworkConfig
	// Doc and ActiveID are the first imported profile, which becomes the
	// document.
	Doc      *model.Doc
	ActiveID string
	Count    int
}

// Message is the import's confirmation.
func (i Import) Message() string { return fmt.Sprintf("已导入 %d 套配置", i.Count) }

// ImportProfiles adds the profiles of an imported file (see ParseImport)
// to profiles, under names of their own, merging their network configs
// into library; every imported profile must emit without errors.
func ImportProfiles(content []byte, platform string, profiles []*model.Profile, library []*model.NetworkConfig, localAddresses []string) (Import, error) {
	incoming, incomingLibrary, err := ParseImport(content, platform)
	if err != nil {
		return Import{}, err
	}
	if len(incoming) == 0 {
		return Import{}, errors.New("没有找到有效配置")
	}
	imported, nextLibrary, err := model.ImportNetworkBundle(incoming, incomingLibrary, library)
	if err != nil {
		return Import{}, err
	}
	merged := slices.Clone(profiles)
	var additions []*model.Profile
	for _, p := range imported {
		base := p.Name
		if base == "" {
			base = "导入配置"
		}
		name := base
		for i := 2; nameTaken(merged, name, ""); i++ {
			name = fmt.Sprintf("%s %d", base, i)
		}
		next := &model.Profile{ID: model.NewID("prof"), Name: name, SavedAt: time.Now().UnixMilli(), Doc: model.FillDefaults(model.Clone(p.Doc))}
		merged = append(merged, next)
		additions = append(additions, next)
	}
	active := additions[0]
	doc, migrated, migratedLibrary := model.MigrateNetworkStore(active.Doc, merged, nextLibrary, active.ID)
	for _, addition := range additions {
		profile := Find(migrated, addition.ID)
		if profile == nil {
			return Import{}, fmt.Errorf("配置「%s」导入后丢失", addition.Name)
		}
		if result := emit.Emit(profile.Doc, emit.Options{NetworkConfigs: migratedLibrary, LocalAddresses: localAddresses}); len(result.Errors) > 0 {
			return Import{}, fmt.Errorf("配置检查失败：%s", strings.Join(result.Errors, "；"))
		}
	}
	return Import{Profiles: migrated, Library: migratedLibrary, Doc: doc, ActiveID: active.ID, Count: len(additions)}, nil
}
