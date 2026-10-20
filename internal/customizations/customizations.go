package customizations

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Skill struct {
	Name        string
	Description string
	Path        string
	Content     string
}

type CustomizationsManager struct {
	skills       []Skill
	ignoredPaths []string
}

func NewCustomizationsManager() *CustomizationsManager {
	return &CustomizationsManager{}
}

// LoadFromWorkspace scans the given root directory for .acodeignore and SKILL.md files.
func (cm *CustomizationsManager) LoadFromWorkspace(root string) error {
	// 1. Load .acodeignore
	ignorePath := filepath.Join(root, ".acodeignore")
	if f, err := os.Open(ignorePath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				cm.ignoredPaths = append(cm.ignoredPaths, line)
			}
		}
	}

	// 2. Discover SKILL.md files
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(d.Name(), "SKILL.md") {
			skill, err := parseSkillFile(path)
			if err == nil {
				cm.skills = append(cm.skills, *skill)
			}
		}
		return nil
	})

	sort.Slice(cm.skills, func(i, j int) bool {
		return cm.skills[i].Name < cm.skills[j].Name
	})

	return nil
}

func parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	content := string(data)
	name := filepath.Base(filepath.Dir(path))
	desc := fmt.Sprintf("Skill at %s", path)

	// Check for YAML-like frontmatter
	if strings.HasPrefix(content, "---") {
		parts := strings.SplitN(content, "---", 3)
		if len(parts) >= 3 {
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "name:") {
					name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				} else if strings.HasPrefix(line, "description:") {
					desc = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
				}
			}
		}
	}

	return &Skill{
		Name:        name,
		Description: desc,
		Path:        path,
		Content:     content,
	}, nil
}

func (cm *CustomizationsManager) Skills() []Skill {
	return cm.skills
}

func (cm *CustomizationsManager) IsIgnored(relPath string) bool {
	for _, pattern := range cm.ignoredPaths {
		if strings.Contains(relPath, pattern) || strings.HasPrefix(relPath, pattern) {
			return true
		}
	}
	return false
}

// GeneratePromptAugmentation generates markdown describing discovered skills.
func (cm *CustomizationsManager) GeneratePromptAugmentation() string {
	if len(cm.skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n\n<custom_skills>\n")
	sb.WriteString("Available project-specific skills:\n")
	for _, s := range cm.skills {
		sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", s.Name, s.Path, s.Description))
	}
	sb.WriteString("</custom_skills>\n")
	return sb.String()
}
