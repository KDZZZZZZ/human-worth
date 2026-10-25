package contract_test

import (
	"fmt"
	"testing"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	challengepb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	discoverypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type plannedMethod struct {
	operation contract.OperationID
	method    string
	reason    string
}

// These policies are intentionally recorded before their RPC contracts exist.
// Remove each exception as soon as its service method is declared in Proto.
var plannedMethods = []plannedMethod{}

func TestOperationMethodsMatchProtoDescriptors(t *testing.T) {
	descriptors := protoMethods(
		discoverypb.File_humanworth_discovery_v1_discovery_proto,
		assetpb.File_humanworth_asset_v1_asset_proto,
		challengepb.File_humanworth_challenge_v1_challenge_proto,
		contentpb.File_humanworth_content_v1_content_proto,
		identitypb.File_humanworth_identity_v1_identity_proto,
		moderationpb.File_humanworth_moderation_v1_moderation_proto,
		votingpb.File_humanworth_voting_v1_voting_proto,
	)
	if err := validateProtoMethods(contract.Operations(), descriptors, plannedMethods); err != nil {
		t.Fatal(err)
	}
}

func protoMethods(files ...protoreflect.FileDescriptor) map[string]struct{} {
	methods := make(map[string]struct{})
	for _, file := range files {
		services := file.Services()
		for i := 0; i < services.Len(); i++ {
			service := services.Get(i)
			for j := 0; j < service.Methods().Len(); j++ {
				methods[fmt.Sprintf("/%s.%s/%s", file.Package(), service.Name(), service.Methods().Get(j).Name())] = struct{}{}
			}
		}
	}
	return methods
}

func validateProtoMethods(operations []contract.Operation, descriptors map[string]struct{}, exceptions []plannedMethod) error {
	byID := make(map[contract.OperationID]contract.Operation, len(operations))
	for _, operation := range operations {
		byID[operation.ID] = operation
	}
	allowed := make(map[contract.OperationID]plannedMethod, len(exceptions))
	for _, exception := range exceptions {
		operation, exists := byID[exception.operation]
		if !exists {
			return fmt.Errorf("planned Proto exception %q is stale: operation is absent", exception.operation)
		}
		if exception.method != operation.FullMethod {
			return fmt.Errorf("planned Proto exception %q names %q, catalog names %q", exception.operation, exception.method, operation.FullMethod)
		}
		if exception.reason == "" {
			return fmt.Errorf("planned Proto exception %q has no reason", exception.operation)
		}
		if _, duplicate := allowed[exception.operation]; duplicate {
			return fmt.Errorf("duplicate planned Proto exception %q", exception.operation)
		}
		if _, declared := descriptors[exception.method]; declared {
			return fmt.Errorf("planned Proto exception %q is obsolete: %q now exists", exception.operation, exception.method)
		}
		allowed[exception.operation] = exception
	}
	for _, operation := range operations {
		if _, exists := descriptors[operation.FullMethod]; exists {
			if _, excepted := allowed[operation.ID]; excepted {
				return fmt.Errorf("planned Proto exception %q is obsolete", operation.ID)
			}
			continue
		}
		if exception, excepted := allowed[operation.ID]; excepted && exception.method == operation.FullMethod {
			continue
		}
		return fmt.Errorf("operation %q references RPC %q absent from Proto descriptors and planned exceptions", operation.ID, operation.FullMethod)
	}
	return nil
}

func TestProtoMethodValidationRejectsCatalogDriftAndExceptionDrift(t *testing.T) {
	baseOperations := []contract.Operation{{ID: "existing", FullMethod: "/example.v1.Service/Call"}}
	descriptors := map[string]struct{}{"/example.v1.Service/Call": {}}
	if err := validateProtoMethods(baseOperations, descriptors, nil); err != nil {
		t.Fatalf("valid method rejected: %v", err)
	}

	tests := []struct {
		name        string
		operations  []contract.Operation
		descriptors map[string]struct{}
		exceptions  []plannedMethod
	}{
		{
			name:        "method typo",
			operations:  []contract.Operation{{ID: "existing", FullMethod: "/example.v1.Service/Calls"}},
			descriptors: descriptors,
		},
		{
			name:        "package typo",
			operations:  []contract.Operation{{ID: "existing", FullMethod: "/example.v2.Service/Call"}},
			descriptors: descriptors,
		},
		{
			name:        "service typo",
			operations:  []contract.Operation{{ID: "existing", FullMethod: "/example.v1.Services/Call"}},
			descriptors: descriptors,
		},
		{
			name:        "undeclared method without exception",
			operations:  append(append([]contract.Operation(nil), baseOperations...), contract.Operation{ID: "new", FullMethod: "/example.v1.Service/NewCall"}),
			descriptors: descriptors,
		},
		{
			name:        "exception method typo",
			operations:  []contract.Operation{{ID: "planned", FullMethod: "/example.v1.Service/Future"}},
			descriptors: descriptors,
			exceptions:  []plannedMethod{{operation: "planned", method: "/example.v1.Service/Futuer", reason: "planned"}},
		},
		{
			name:        "exception expires when method is declared",
			operations:  []contract.Operation{{ID: "planned", FullMethod: "/example.v1.Service/Future"}},
			descriptors: map[string]struct{}{"/example.v1.Service/Future": {}},
			exceptions:  []plannedMethod{{operation: "planned", method: "/example.v1.Service/Future", reason: "planned"}},
		},
		{
			name:        "exception expires when operation is removed",
			descriptors: descriptors,
			exceptions:  []plannedMethod{{operation: "planned", method: "/example.v1.Service/Future", reason: "planned"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateProtoMethods(test.operations, test.descriptors, test.exceptions); err == nil {
				t.Fatal("invalid catalog/exception state was accepted")
			}
		})
	}
}
