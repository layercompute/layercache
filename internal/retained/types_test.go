package retained

import (
	"strings"
	"testing"
	"time"
)

func validDeclaration() Declaration {
	return Declaration{Producer: Producer{Project: "private", Repository: "acme/repo", RepositoryID: "42", SourceCommit: strings.Repeat("a", 40), WorkflowRef: "acme/repo/.github/workflows/check.yml@refs/heads/main", RunID: "1", RunAttempt: "1", JobID: "2", Profile: "debug"}, Name: "apk", Digest: strings.Repeat("b", 64), Size: 1, RetainUntil: time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)}
}
func TestDeclarationRejectsInexactProducerAndDeadline(t *testing.T) {
	valid := validDeclaration()
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Declaration){func(d *Declaration) { d.RetainUntil = d.RetainUntil.Add(time.Nanosecond) }, func(d *Declaration) { d.Producer.JobID = "compile" }, func(d *Declaration) { d.Producer.SourceCommit = strings.Repeat("a", 41) }, func(d *Declaration) { d.Producer.RunID = "0" }, func(d *Declaration) { d.Name = "\nforeign" }} {
		d := valid
		change(&d)
		if err := Validate(d); err == nil {
			t.Fatalf("accepted invalid declaration %+v", d)
		}
	}
}
func TestPublicationIdentityDistinguishesProducerAttemptAndName(t *testing.T) {
	d := validDeclaration()
	id := ID(d)
	if !ValidID(id) {
		t.Fatal("invalid identity")
	}
	changed := d
	changed.Digest = strings.Repeat("c", 64)
	changed.Size++
	changed.RetainUntil = changed.RetainUntil.Add(time.Hour)
	if ID(changed) != id {
		t.Fatal("content/deadline changes must conflict at existing identity")
	}
	changed = d
	changed.Producer.RunAttempt = "2"
	if ID(changed) == id {
		t.Fatal("attempt identity collision")
	}
	changed = d
	changed.Name = "other"
	if ID(changed) == id {
		t.Fatal("name identity collision")
	}
}
