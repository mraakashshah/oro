package beadstore_test

import (
	"context"
	"reflect"
	"testing"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestStoreContractMatchesReplatformSpec(t *testing.T) {
	storeType := reflect.TypeOf((*beadstore.Store)(nil)).Elem()

	methods := map[string]reflect.Type{}
	for i := range storeType.NumMethod() {
		method := storeType.Method(i)
		methods[method.Name] = method.Type
	}

	if len(methods) != 22 {
		t.Fatalf("Store has %d methods, want 22: %v", len(methods), methodNames(methods))
	}

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	beadSliceType := reflect.TypeOf([]protocol.Bead{})
	beadPtrSliceType := reflect.TypeOf([]*protocol.Bead{})
	beadPtrType := reflect.TypeOf((*protocol.Bead)(nil))
	bytesType := reflect.TypeOf([]byte{})
	errType := reflect.TypeOf((*error)(nil)).Elem()
	stringType := reflect.TypeOf("")
	intType := reflect.TypeOf(0)
	boolType := reflect.TypeOf(false)
	createParamsType := reflect.TypeOf(beadstore.CreateParams{})
	updateParamsType := reflect.TypeOf(beadstore.UpdateParams{})
	cycleSliceType := reflect.TypeOf([]beadstore.Cycle{})

	assertSignature(t, methods, "Ready", []reflect.Type{ctxType}, []reflect.Type{beadSliceType, errType})
	assertSignature(t, methods, "InProgress", []reflect.Type{ctxType}, []reflect.Type{beadSliceType, errType})
	assertSignature(t, methods, "Blocked", []reflect.Type{ctxType}, []reflect.Type{beadSliceType, errType})
	assertSignature(t, methods, "Closed", []reflect.Type{ctxType, intType}, []reflect.Type{beadSliceType, errType})
	assertSignature(t, methods, "Show", []reflect.Type{ctxType, stringType}, []reflect.Type{beadPtrType, errType})
	assertSignature(t, methods, "Create", []reflect.Type{ctxType, createParamsType}, []reflect.Type{beadPtrType, errType})
	assertSignature(t, methods, "Update", []reflect.Type{ctxType, stringType, updateParamsType}, []reflect.Type{errType})
	assertSignature(t, methods, "UpdateStatusIf", []reflect.Type{ctxType, stringType, stringType, stringType}, []reflect.Type{boolType, errType})
	assertSignature(t, methods, "Close", []reflect.Type{ctxType, stringType, stringType}, []reflect.Type{errType})
	assertSignature(t, methods, "Delete", []reflect.Type{ctxType, stringType, stringType}, []reflect.Type{errType})
	assertSignature(t, methods, "HasChildren", []reflect.Type{ctxType, stringType}, []reflect.Type{boolType, errType})
	assertSignature(t, methods, "AllChildrenClosed", []reflect.Type{ctxType, stringType}, []reflect.Type{boolType, errType})
	assertSignature(t, methods, "FindByParentAndTag", []reflect.Type{ctxType, stringType, stringType}, []reflect.Type{beadSliceType, errType})
	assertSignature(t, methods, "FindByMetadataKey", []reflect.Type{ctxType, stringType}, []reflect.Type{beadPtrSliceType, errType})
	assertSignature(t, methods, "Export", []reflect.Type{ctxType}, []reflect.Type{bytesType, errType})
	assertSignature(t, methods, "CountChildren", []reflect.Type{ctxType, stringType}, []reflect.Type{intType, errType})
	assertSignature(t, methods, "DependencyCycles", []reflect.Type{ctxType}, []reflect.Type{cycleSliceType, errType})
}

func TestStoreInterfaceDeleteSignature(t *testing.T) {
	storeType := reflect.TypeOf((*beadstore.Store)(nil)).Elem()
	method, ok := storeType.MethodByName("Delete")
	if !ok {
		t.Fatal("Store missing Delete method")
	}

	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	stringType := reflect.TypeOf("")
	errType := reflect.TypeOf((*error)(nil)).Elem()
	assertSignature(t, map[string]reflect.Type{"Delete": method.Type}, "Delete",
		[]reflect.Type{ctxType, stringType, stringType},
		[]reflect.Type{errType})
}

func TestCreateAndUpdateParamsExposeSpecFields(t *testing.T) {
	title := "Updated title"
	description := "Updated description"
	status := "in_progress"
	priority := 1
	issueType := "bug"
	acceptance := "updated acceptance"
	estimate := 5
	contractVersion := 2
	draft := true
	parentID := "oro-parent"
	owner := "aakash"
	notes := "updated notes"

	create := beadstore.CreateParams{
		ID:                 "oro-test",
		Title:              "Define store interface",
		Type:               "task",
		Priority:           1,
		Description:        "description",
		AcceptanceCriteria: "acceptance",
		ParentID:           "oro-parent",
		Tags:               []string{"phase-1"},
		Labels:             []string{"store"},
		Metadata:           map[string]string{"source": "migration"},
		EstimatedMinutes:   60,
	}

	update := beadstore.UpdateParams{
		Title:              &title,
		Description:        &description,
		Status:             &status,
		Priority:           &priority,
		Type:               &issueType,
		AcceptanceCriteria: &acceptance,
		EstimatedMinutes:   &estimate,
		ContractVersion:    &contractVersion,
		Draft:              &draft,
		ParentID:           &parentID,
		Owner:              &owner,
		Notes:              &notes,
	}

	if create.ContractVersion != 0 || create.Draft || create.ID != "oro-test" ||
		create.Title != "Define store interface" ||
		create.Type != "task" ||
		create.Priority != 1 ||
		create.Description != "description" ||
		create.AcceptanceCriteria != "acceptance" ||
		create.ParentID != "oro-parent" ||
		create.EstimatedMinutes != 60 ||
		len(create.Tags) != 1 ||
		create.Tags[0] != "phase-1" ||
		len(create.Labels) != 1 ||
		create.Labels[0] != "store" ||
		create.Metadata["source"] != "migration" {
		t.Fatalf("CreateParams did not retain expected fields: %#v", create)
	}
	if update.Title == nil ||
		*update.Title != title ||
		update.Description == nil ||
		*update.Description != description ||
		update.Status == nil ||
		*update.Status != "in_progress" ||
		update.Priority == nil ||
		*update.Priority != 1 ||
		update.Type == nil ||
		*update.Type != "bug" ||
		update.AcceptanceCriteria == nil ||
		*update.AcceptanceCriteria != acceptance ||
		update.EstimatedMinutes == nil ||
		*update.EstimatedMinutes != estimate ||
		update.ContractVersion == nil ||
		*update.ContractVersion != contractVersion ||
		update.Draft == nil ||
		!*update.Draft ||
		update.ParentID == nil ||
		*update.ParentID != "oro-parent" ||
		update.Owner == nil ||
		*update.Owner != "aakash" ||
		update.Notes == nil ||
		*update.Notes != notes {
		t.Fatalf("UpdateParams did not retain expected pointer fields: %#v", update)
	}
}

func assertSignature(t *testing.T, methods map[string]reflect.Type, name string, inputs, outputs []reflect.Type) {
	t.Helper()

	methodType, ok := methods[name]
	if !ok {
		t.Fatalf("Store missing method %s", name)
	}

	if methodType.NumIn() != len(inputs) {
		t.Fatalf("%s has %d inputs, want %d", name, methodType.NumIn(), len(inputs))
	}
	for i, want := range inputs {
		if got := methodType.In(i); got != want {
			t.Fatalf("%s input %d is %s, want %s", name, i, got, want)
		}
	}

	if methodType.NumOut() != len(outputs) {
		t.Fatalf("%s has %d outputs, want %d", name, methodType.NumOut(), len(outputs))
	}
	for i, want := range outputs {
		if got := methodType.Out(i); got != want {
			t.Fatalf("%s output %d is %s, want %s", name, i, got, want)
		}
	}
}

func methodNames(methods map[string]reflect.Type) []string {
	names := make([]string, 0, len(methods))
	for name := range methods {
		names = append(names, name)
	}
	return names
}
