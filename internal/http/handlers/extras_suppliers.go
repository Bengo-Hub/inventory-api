package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/Bengo-Hub/pagination"
	"github.com/bengobox/inventory-service/internal/ent"
	entsupplier "github.com/bengobox/inventory-service/internal/ent/supplier"
	"github.com/bengobox/inventory-service/internal/platform/subscriptions"
)

// ─── Suppliers ────────────────────────────────────────────────────────────────

type supplierDTO struct {
	ID                           uuid.UUID `json:"id"`
	Name                         string    `json:"name"`
	Code                         string    `json:"code"`
	ContactPerson                string    `json:"contact_person"`
	Email                        string    `json:"email"`
	Phone                        string    `json:"phone"`
	Address                      string    `json:"address"`
	IsActive                     bool      `json:"is_active"`
	PaymentMethodType            string    `json:"payment_method_type"`
	MpesaPhone                   string    `json:"mpesa_phone"`
	MpesaBusinessName            string    `json:"mpesa_business_name"`
	BankAccountNumber            string    `json:"bank_account_number"`
	BankName                     string    `json:"bank_name"`
	BankBranch                   string    `json:"bank_branch"`
	TaxPin                       string    `json:"tax_pin"`
	RequiresInvoiceBeforePayment bool      `json:"requires_invoice_before_payment"`
	AutoPayEnabled               bool      `json:"auto_pay_enabled"`
	PaymentTermsDays             int       `json:"payment_terms_days"`
	CreditLimit                  *float64  `json:"credit_limit"`
	CreatedAt                    time.Time `json:"created_at"`
}

type supplierPayload struct {
	Name                         string  `json:"name"`
	ContactPerson                string  `json:"contact_person"`
	Email                        string  `json:"email"`
	Phone                        string  `json:"phone"`
	Address                      string  `json:"address"`
	PaymentMethodType            string  `json:"payment_method_type"`
	MpesaPhone                   string  `json:"mpesa_phone"`
	MpesaBusinessName            string  `json:"mpesa_business_name"`
	BankAccountNumber            string  `json:"bank_account_number"`
	BankName                     string  `json:"bank_name"`
	BankBranch                   string  `json:"bank_branch"`
	TaxPin                       string  `json:"tax_pin"`
	RequiresInvoiceBeforePayment bool    `json:"requires_invoice_before_payment"`
	AutoPayEnabled               bool    `json:"auto_pay_enabled"`
	PaymentTermsDays             int     `json:"payment_terms_days"`
	CreditLimit                  float64 `json:"credit_limit"`
}

func supplierToDTO(s *ent.Supplier) supplierDTO {
	dto := supplierDTO{
		ID:                           s.ID,
		Name:                         s.Name,
		Code:                         s.Code,
		ContactPerson:                s.ContactName,
		Email:                        s.ContactEmail,
		Phone:                        s.ContactPhone,
		Address:                      s.Address,
		IsActive:                     s.IsActive,
		PaymentMethodType:            string(s.PaymentMethodType),
		MpesaPhone:                   s.MpesaPhone,
		MpesaBusinessName:            s.MpesaBusinessName,
		BankAccountNumber:            s.BankAccountNumber,
		BankName:                     s.BankName,
		BankBranch:                   s.BankBranch,
		TaxPin:                       s.TaxPin,
		RequiresInvoiceBeforePayment: s.RequiresInvoiceBeforePayment,
		AutoPayEnabled:               s.AutoPayEnabled,
		PaymentTermsDays:             s.PaymentTermsDays,
		CreatedAt:                    s.CreatedAt,
	}
	if s.CreditLimit != 0 {
		v := s.CreditLimit
		dto.CreditLimit = &v
	}
	return dto
}

func (h *InventoryExtrasHandler) ListSuppliers(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	p := pagination.Parse(r)
	search := r.URL.Query().Get("search")

	q := h.orm.Supplier.Query().Where(entsupplier.TenantID(tenantID))
	// Exclude soft-deleted suppliers (DeleteSupplier sets is_active=false) by default so a
	// "deleted" supplier no longer appears in lists/pickers. Pass ?include_inactive=true to
	// include them (e.g. an admin/archive view).
	if r.URL.Query().Get("include_inactive") != "true" {
		q = q.Where(entsupplier.IsActive(true))
	}
	if search != "" {
		q = q.Where(entsupplier.Or(
			entsupplier.NameContainsFold(search),
			entsupplier.ContactNameContainsFold(search),
		))
	}
	total, _ := q.Clone().Count(r.Context())
	suppliers, err := q.Order(ent.Asc(entsupplier.FieldName)).Limit(p.Limit).Offset(p.Offset).All(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL", "Failed to list suppliers")
		return
	}

	result := make([]supplierDTO, len(suppliers))
	for i, s := range suppliers {
		result[i] = supplierToDTO(s)
	}
	writeJSON(w, http.StatusOK, pagination.NewResponse(result, total, p))
}

func (h *InventoryExtrasHandler) CreateSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	var req supplierPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Supplier name is required")
		return
	}

	// Enforce the plan's max_suppliers structural cap (hard-block, no overage).
	if count, cerr := h.orm.Supplier.Query().Where(entsupplier.TenantID(tenantID)).Count(r.Context()); cerr == nil {
		if subscriptions.AssertLimit(w, r, "suppliers", subscriptions.LimitSuppliers, count) {
			return
		}
	}

	code := strings.ToUpper(strings.ReplaceAll(req.Name, " ", "_"))
	if len(code) > 20 {
		code = code[:20]
	}

	create := h.orm.Supplier.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetCode(code).
		SetContactName(req.ContactPerson).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		SetAddress(req.Address).
		SetRequiresInvoiceBeforePayment(req.RequiresInvoiceBeforePayment).
		SetAutoPayEnabled(req.AutoPayEnabled).
		SetPaymentTermsDays(req.PaymentTermsDays).
		SetMpesaPhone(req.MpesaPhone).
		SetMpesaBusinessName(req.MpesaBusinessName).
		SetBankAccountNumber(req.BankAccountNumber).
		SetBankName(req.BankName).
		SetBankBranch(req.BankBranch).
		SetTaxPin(req.TaxPin)

	if req.PaymentMethodType != "" {
		create = create.SetPaymentMethodType(entsupplier.PaymentMethodType(req.PaymentMethodType))
	}
	if req.CreditLimit > 0 {
		create = create.SetCreditLimit(req.CreditLimit)
	}

	s, err := create.Save(r.Context())
	if err != nil {
		h.log.Error("create supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "Failed to create supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.created")
	writeJSON(w, http.StatusCreated, supplierToDTO(s))
}

func (h *InventoryExtrasHandler) GetSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	s, err := h.orm.Supplier.Query().
		Where(entsupplier.ID(supplierID), entsupplier.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}
	writeJSON(w, http.StatusOK, supplierToDTO(s))
}

func (h *InventoryExtrasHandler) UpdateSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	existing, err := h.orm.Supplier.Query().
		Where(entsupplier.ID(supplierID), entsupplier.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}

	var req supplierPayload
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Invalid request body")
		return
	}

	update := h.orm.Supplier.UpdateOneID(existing.ID).
		SetContactName(req.ContactPerson).
		SetContactEmail(req.Email).
		SetContactPhone(req.Phone).
		SetAddress(req.Address).
		SetRequiresInvoiceBeforePayment(req.RequiresInvoiceBeforePayment).
		SetAutoPayEnabled(req.AutoPayEnabled).
		SetPaymentTermsDays(req.PaymentTermsDays).
		SetMpesaPhone(req.MpesaPhone).
		SetMpesaBusinessName(req.MpesaBusinessName).
		SetBankAccountNumber(req.BankAccountNumber).
		SetBankName(req.BankName).
		SetBankBranch(req.BankBranch).
		SetTaxPin(req.TaxPin)

	if req.Name != "" {
		update = update.SetName(req.Name)
	}
	if req.PaymentMethodType != "" {
		update = update.SetPaymentMethodType(entsupplier.PaymentMethodType(req.PaymentMethodType))
	}
	if req.CreditLimit > 0 {
		update = update.SetCreditLimit(req.CreditLimit)
	}

	s, err := update.Save(r.Context())
	if err != nil {
		h.log.Error("update supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "UPDATE_FAILED", "Failed to update supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), s, "inventory.supplier.updated")
	writeJSON(w, http.StatusOK, supplierToDTO(s))
}

// DeleteSupplier handles DELETE /inventory/suppliers/{supplierID} — soft-deletes a supplier.
func (h *InventoryExtrasHandler) DeleteSupplier(w http.ResponseWriter, r *http.Request) {
	tenantID, err := parseTenantID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_TENANT", "Invalid tenant ID")
		return
	}
	supplierID, err := uuid.Parse(chi.URLParam(r, "supplierID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "Invalid supplier ID")
		return
	}
	existing, err := h.orm.Supplier.Get(r.Context(), supplierID)
	if err != nil || existing.TenantID != tenantID {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Supplier not found")
		return
	}
	updated, err := h.orm.Supplier.UpdateOneID(supplierID).SetIsActive(false).Save(r.Context())
	if err != nil {
		h.log.Error("delete supplier failed", zap.Error(err))
		writeError(w, http.StatusInternalServerError, "DELETE_FAILED", "Failed to delete supplier")
		return
	}
	h.publishSupplierEvent(r.Context(), updated, "inventory.supplier.deleted")
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
