package mcpserver

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A recipe is a sequence that worked once in an app, saved so the same
// situation next time takes one act call instead of a look-plan-act loop per
// step. Coordinates are stored in window points, so a recipe follows the
// window wherever it is; it runs only on a window of the size it was saved
// at, since a different size reflows the content. Typed text is stored as
// {{placeholders}} filled at run time: recipes keep no personal text.

const maxRecipes = 30

type recipe struct {
	Name    string   `json:"name"`
	When    string   `json:"when" jsonschema:"the situation it is for, e.g. a Safari window with any page open"`
	WindowW float64  `json:"window_width"`
	WindowH float64  `json:"window_height"`
	Steps   []step   `json:"steps" jsonschema:"x, y and region in window points"`
	Params  []string `json:"params,omitempty" jsonschema:"placeholders to fill with args"`
	Saved   string   `json:"saved"`
	Runs    int      `json:"runs"`
	Fails   int      `json:"fails"`
}

func readRecipes(app string) ([]recipe, error) {
	path, err := appFile(app, ".recipes.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []recipe
	return out, json.Unmarshal(b, &out)
}

func writeRecipes(app string, rs []recipe) error {
	path, err := appFile(app, ".recipes.json")
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

var placeholder = regexp.MustCompile(`\{\{([a-z0-9_]+)\}\}`)

// newRecipe converts steps read from shots of one window into a recipe.
// Typed text becomes a placeholder unless it already holds placeholders or
// keepText is set.
func newRecipe(reg *shots, name, when string, steps []step, defShot int, keepText bool) (recipe, *shot, error) {
	if !placeholder.MatchString("{{" + name + "}}") {
		return recipe{}, nil, fmt.Errorf("name a recipe with lowercase letters, digits and _, e.g. search_web")
	}
	if strings.TrimSpace(when) == "" {
		return recipe{}, nil, fmt.Errorf("say when the recipe applies (when), so a later task can tell")
	}
	if len(steps) == 0 || len(steps) > maxSteps {
		return recipe{}, nil, fmt.Errorf("a recipe has 1 to %d steps", maxSteps)
	}
	base, err := reg.get(defShot)
	if err != nil {
		return recipe{}, nil, err
	}
	if base.Window == 0 {
		return recipe{}, nil, fmt.Errorf("recipes are saved from shots of an app window, not the display")
	}
	rc := recipe{Name: name, When: strings.Join(strings.Fields(when), " "), WindowW: base.FW, WindowH: base.FH, Saved: time.Now().Format("2006-01-02")}
	seen := map[string]bool{}
	for i, s := range steps {
		if s.Shot == 0 {
			s.Shot = defShot
		}
		sh, err := reg.get(s.Shot)
		if err != nil {
			return recipe{}, nil, fmt.Errorf("step %d: %v", i+1, err)
		}
		if sh.Window != base.Window {
			return recipe{}, nil, fmt.Errorf("step %d: shot %d is of another window; a recipe stays in one window", i+1, sh.ID)
		}
		if s.X != nil {
			x, y := sh.QX+(*s.X+0.5)*sh.Scale, sh.QY+(*s.Y+0.5)*sh.Scale
			s.X, s.Y = &x, &y
			s.Size *= sh.Scale
		}
		if len(s.Region) == 4 {
			s.Region = []float64{sh.QX + s.Region[0]*sh.Scale, sh.QY + s.Region[1]*sh.Scale, s.Region[2] * sh.Scale, s.Region[3] * sh.Scale}
		}
		s.Shot = 0
		if s.Do == "type" && !keepText && !placeholder.MatchString(s.Text) {
			s.Text = fmt.Sprintf("{{text%d}}", i+1)
		}
		for _, m := range placeholder.FindAllStringSubmatch(s.Text, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				rc.Params = append(rc.Params, m[1])
			}
		}
		rc.Steps = append(rc.Steps, s)
	}
	return rc, base, nil
}

// expand turns a recipe into steps in the pixels of sh, a whole-window shot.
func (rc recipe) expand(sh *shot, args map[string]string) ([]step, error) {
	if sh.Window == 0 || sh.Cropped {
		return nil, fmt.Errorf("run a recipe with a whole-window shot of the app (not a zoom)")
	}
	if math.Abs(sh.FW-rc.WindowW) > 2 || math.Abs(sh.FH-rc.WindowH) > 2 {
		return nil, fmt.Errorf("recipe %s was saved on a %.0fx%.0f window; this one is %.0fx%.0f, so positions moved. Act from the shot, then save the recipe again",
			rc.Name, rc.WindowW, rc.WindowH, sh.FW, sh.FH)
	}
	var missing []string
	out := make([]step, len(rc.Steps))
	for i, s := range rc.Steps {
		s.Shot = sh.ID
		if s.X != nil {
			x, y := (*s.X-sh.QX)/sh.Scale-0.5, (*s.Y-sh.QY)/sh.Scale-0.5
			s.X, s.Y = &x, &y
			s.Size /= sh.Scale
		}
		if len(s.Region) == 4 {
			s.Region = []float64{(s.Region[0] - sh.QX) / sh.Scale, (s.Region[1] - sh.QY) / sh.Scale, s.Region[2] / sh.Scale, s.Region[3] / sh.Scale}
		}
		s.Text = placeholder.ReplaceAllStringFunc(s.Text, func(m string) string {
			k := m[2 : len(m)-2]
			v, ok := args[k]
			if !ok {
				missing = append(missing, k)
			}
			return v
		})
		out[i] = s
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("recipe %s needs args: %s", rc.Name, strings.Join(missing, ", "))
	}
	return out, nil
}

// saveRecipe adds rc to app's recipes, replacing one of the same name.
func saveRecipe(app string, rc recipe) ([]recipe, error) {
	rs, err := readRecipes(app)
	if err != nil {
		return nil, err
	}
	kept := rs[:0]
	for _, r := range rs {
		if r.Name != rc.Name {
			kept = append(kept, r)
		}
	}
	kept = append(kept, rc)
	if len(kept) > maxRecipes {
		return nil, fmt.Errorf("%s has %d recipes; delete unused ones first", app, maxRecipes)
	}
	return kept, writeRecipes(app, kept)
}

func findRecipe(app, name string) (recipe, error) {
	rs, err := readRecipes(app)
	if err != nil {
		return recipe{}, err
	}
	for _, r := range rs {
		if r.Name == name {
			return r, nil
		}
	}
	return recipe{}, fmt.Errorf("no recipe %q for %s; list them with recipes", name, app)
}

// countRun records how a run of recipe name went.
func countRun(app, name string, failed bool) {
	rs, err := readRecipes(app)
	if err != nil {
		return
	}
	for i := range rs {
		if rs[i].Name == name {
			rs[i].Runs++
			if failed {
				rs[i].Fails++
			}
		}
	}
	writeRecipes(app, rs)
}

// summary is one line per recipe, for a screenshot's notes.
func (rc recipe) summary() string {
	s := rc.Name
	if len(rc.Params) > 0 {
		s += "(" + strings.Join(rc.Params, ", ") + ")"
	}
	s += ": " + rc.When
	if rc.Fails > 0 {
		s += fmt.Sprintf(" [failed %d of %d runs]", rc.Fails, rc.Runs)
	}
	return s
}
