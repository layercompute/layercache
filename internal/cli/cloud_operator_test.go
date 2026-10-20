package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/portal"
	"github.com/layercache/layercache/internal/server"
)

func TestCloudOperatorAdoptionLoadsOnlyProtectedProjectAndRegistryFiles(t *testing.T) {
	root := t.TempDir()
	template, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	templatePath := filepath.Join(root, "template.json")
	if err := config.Save(templatePath, template); err != nil {
		t.Fatal(err)
	}
	project := template
	project.Role, project.ProjectID = "team", "github.com/example/app"
	projectPath := filepath.Join(root, "project.json")
	if err := config.Save(projectPath, project); err != nil {
		t.Fatal(err)
	}
	secret, key, registry := filepath.Join(root, "oauth"), filepath.Join(root, "key"), filepath.Join(root, "registry")
	for path, data := range map[string][]byte{secret: []byte("fixture-oauth"), key: bytes.Repeat([]byte{42}, 32), registry: []byte("fixture-registry-secret")} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	file := cloudFile{
		Listen: "127.0.0.1:8080", Origin: "https://cloud.example", DataDir: filepath.Join(root, "data"),
		GitHubClientID: "client", TeamCreatorIDs: []string{"123"}, GitHubClientSecretFile: secret, SessionKeyFile: key,
		ProjectTemplateFile: templatePath, StoragePool: server.StoragePoolConfig{Path: root, MaxBytes: 1 << 30},
		RegistryURL: "http://127.0.0.1:5000", RegistryUsername: "backend", RegistryPasswordFile: registry,
		OperatorProjects: []operatorProjectFile{{ProjectConfigFile: projectPath, Name: "Example", RepositoryID: "456", Owner: portal.User{ID: "123", Login: "example"}, TeamID: "operator-example", TeamName: "Example projects", Alias: "example-app"}},
	}
	path := filepath.Join(root, "cloud.json")
	save := func() {
		t.Helper()
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	_, loaded, err := loadCloud(path)
	if err != nil || len(loaded.OperatorProjects) != 1 {
		t.Fatal("operator config not loaded", err)
	}
	p := loaded.OperatorProjects[0]
	if p.Config.ProjectID != project.ProjectID || p.Config.LocalToken != project.LocalToken || p.Owner.ID != "123" || p.Alias != "example-app" || loaded.RegistryPassword != "fixture-registry-secret" {
		t.Fatal("operator identity or protected registry mapping changed")
	}
	for _, protected := range []string{projectPath, registry} {
		if err := os.Chmod(protected, 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadCloud(path); err == nil {
			t.Fatal("accepted unprotected operator input")
		}
		if err := os.Chmod(protected, 0600); err != nil {
			t.Fatal(err)
		}
	}
	file.OperatorProjects[0].ProjectConfigFile = "relative.json"
	save()
	if _, _, err := loadCloud(path); err == nil {
		t.Fatal("accepted relative operator project path")
	}
}

func TestLoadProjectGatewayExistingMembershipPreservationIsExplicit(t *testing.T) {
	root := t.TempDir()
	project, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	project.Role = "team"
	projectPath := filepath.Join(root, "project.json")
	if err := config.Save(projectPath, project); err != nil {
		t.Fatal(err)
	}
	file := projectGatewayFile{Listen: "127.0.0.1:7437", Origin: "https://cache.example", Projects: map[string]string{"alpha": projectPath}}
	path := filepath.Join(root, "gateway.json")
	for _, preserve := range []bool{false, true} {
		file.PreserveExistingMemberships = preserve
		data, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, loaded, err := loadProjectGateway(path)
		if err != nil || loaded.PreserveExistingMemberships != preserve || loaded.Projects["alpha"].LocalToken != project.LocalToken {
			t.Fatal("existing membership mode or original signing identity changed", err)
		}
	}
}
