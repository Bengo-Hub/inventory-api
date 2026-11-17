package items

import (
	"strings"

	"github.com/bengobox/inventory-service/internal/ent/item"
)

// outletItemUseCases maps an outlet use_case (the warehouse mirror of auth's outlet use_case) to
// the item use_cases that belong on that outlet's catalog. It is the Go copy of itemUseCases in
// inventory-ui src/lib/use-case-nomenclature.ts, so the back office and the server agree on what
// an outlet sells. Keep the two in step.
//
// An outlet use_case missing from the map (HQ, general, wholesale, hospital, or none) is not
// restricted, the same as the UI's DEFAULT_SCOPE.
var outletItemUseCases = map[string][]item.UseCase{
	"hospitality": {
		item.UseCaseFOOD_BEVERAGE, item.UseCaseHOSPITALITY_ROOM, item.UseCaseHOSPITALITY_FACILITY,
		item.UseCaseCONFERENCE, item.UseCaseAMENITY, item.UseCaseRETAIL,
	},
	"quick_service": {item.UseCaseFOOD_BEVERAGE, item.UseCaseRETAIL},
	"retail":        {item.UseCaseRETAIL},
	"manufacturing": {item.UseCaseRETAIL},
	"pharmacy":      {item.UseCasePHARMACY},
	"services": {
		item.UseCasePROFESSIONAL_SERVICE, item.UseCasePRINTING_SERVICE, item.UseCaseSALON_SERVICE,
		item.UseCaseNAIL_SERVICE, item.UseCaseSPA_SERVICE, item.UseCaseAUTO_SERVICE,
		item.UseCaseLAUNDRY_SERVICE, item.UseCaseTAILORING_SERVICE, item.UseCaseREPAIR_SERVICE,
		item.UseCaseAMENITY,
	},
	"warehouse": {item.UseCaseRETAIL, item.UseCaseFOOD_BEVERAGE, item.UseCasePHARMACY},
}

// AllowedItemUseCases returns the item use_cases an outlet of the given use_case sells, and false
// when the outlet is not restricted.
//
// PROFESSIONAL_SERVICE is the generic service bucket (a delivery charge, a consultation, an
// installation fee), so it is allowed at every restricted outlet, not only services outlets.
// A service item nobody tagged carries the RETAIL default, which every selling outlet except
// pharmacy and services already allows.
func AllowedItemUseCases(outletUseCase string) ([]item.UseCase, bool) {
	list, ok := outletItemUseCases[strings.ToLower(strings.TrimSpace(outletUseCase))]
	if !ok {
		return nil, false
	}
	out := make([]item.UseCase, 0, len(list)+1)
	out = append(out, list...)
	hasProfessional := false
	for _, uc := range list {
		if uc == item.UseCasePROFESSIONAL_SERVICE {
			hasProfessional = true
			break
		}
	}
	if !hasProfessional {
		out = append(out, item.UseCasePROFESSIONAL_SERVICE)
	}
	return out, true
}
