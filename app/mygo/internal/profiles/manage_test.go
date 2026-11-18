package profiles

import (
	"errors"
	"os"
	"testing"

	"github.com/getkiko/kiko/app/mygo/internal/model"
)

func profile(id, name string, savedAt int64) *model.Profile {
	return &model.Profile{ID: id, Name: name, SavedAt: savedAt, Doc: model.EmptyDoc("linux")}
}

func TestSummary(t *testing.T) {
	doc := &model.Doc{Inbounds: []*model.Inbound{{}}, Outbounds: []*model.Outbound{{}, {}}}
	if got := Summary(doc); got != "1 入 · 2 出 · 0 规则" {
		t.Errorf("summary %q", got)
	}
	doc.Subscriptions = []*model.Subscription{{}}
	if got := Summary(doc); got != "1 入 · 2 出 · 1 订 · 0 规则" {
		t.Errorf("summary %q", got)
	}
}

func TestNamesAndDuplicates(t *testing.T) {
	list := []*model.Profile{profile("a", "家里", 1), profile("b", "家里 副本", 2)}
	if _, err := NewProfile(list, "  ", "linux"); !errors.Is(err, ErrNameRequired) {
		t.Errorf("blank name: %v", err)
	}
	if _, err := NewProfile(list, " 家里 ", "linux"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("taken name: %v", err)
	}
	if p, err := NewProfile(list, " 公司 ", "darwin"); err != nil || p.Name != "公司" || p.Doc.Platform != "darwin" {
		t.Errorf("new %v %v", p, err)
	}
	if name, err := CheckName(list, "家里", "a"); err != nil || name != "家里" {
		t.Errorf("own name %q %v", name, err)
	}
	if got := Duplicate(list, list[0]).Name; got != "家里 副本 2" {
		t.Errorf("duplicate %q", got)
	}
	if got := Renamed(list, "a", "公司"); got[0].Name != "公司" || list[0].Name != "家里" {
		t.Errorf("rename %v", got[0].Name)
	}
	if sorted := Sorted(list); sorted[0].ID != "b" {
		t.Errorf("sorted %v", sorted[0].ID)
	}
}

func TestDelete(t *testing.T) {
	list := []*model.Profile{profile("a", "A", 1), profile("b", "B", 3), profile("c", "C", 2)}
	d, err := Delete(list, "c", "a", "linux", nil)
	if err != nil || len(d.Profiles) != 2 || d.ActiveID != "a" || d.Doc != nil {
		t.Errorf("inactive: %+v %v", d, err)
	}
	d, err = Delete(list, "a", "a", "linux", nil)
	if err != nil || d.ActiveID != "b" || d.Doc == nil {
		t.Errorf("active: %+v %v", d, err)
	}
	d, err = Delete(list[:1], "a", "a", "darwin", nil)
	if err != nil || len(d.Profiles) != 1 || d.Profiles[0].Name != "未命名配置" || d.ActiveID != d.Profiles[0].ID || d.Doc.Platform != "darwin" {
		t.Errorf("last: %+v %v", d, err)
	}
}

func TestImportProfilesRenamesAndActivates(t *testing.T) {
	backup, err := os.ReadFile("../workspaces/overview/testdata/backup.json")
	if err != nil {
		t.Fatal(err)
	}
	existing := []*model.Profile{profile("a", "备份配置", 1)}
	result, err := ImportProfiles(backup, "linux", existing, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 || len(result.Profiles) != 2 || result.Message() != "已导入 1 套配置" {
		t.Fatalf("result %+v", result)
	}
	added := result.Profiles[1]
	if added.Name != "备份配置 2" || added.ID == "prof_backup" || result.ActiveID != added.ID || result.Doc.Inbounds[0].Tag != "导入入站" {
		t.Errorf("added %s active %q", asJSON(t, added), result.ActiveID)
	}
	if _, err := ImportProfiles([]byte(`{"name": "x"}`), "linux", existing, nil, nil); err == nil || err.Error() != "没有找到有效配置" {
		t.Errorf("empty import: %v", err)
	}
}
