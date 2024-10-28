package filter

import (
	"context"

	"github.com/stepandra/anton/abi"
	"github.com/stepandra/anton/addr"
	"github.com/stepandra/anton/internal/core"
)

type LabelsReq struct {
	Name       string               `form:"name"`
	Categories []core.LabelCategory `form:"category"`
	Offset     int                  `form:"offset"`
	Limit      int                  `form:"limit"`
}

type LabelsRes struct {
	Total int                  `json:"total"`
	Rows  []*core.AddressLabel `json:"results"`
}

type AccountsReq struct {
	WithCodeData bool

	Addresses   []*addr.Address // `form:"addresses"`
	LatestState bool            `form:"latest"`

	StateIDs []*core.AccountStateID

	// filter by block
	Workchain     *int32
	Shard         *int64
	BlockSeqNoLeq *uint32
	BlockSeqNoBeq *uint32

	// contract data filter
	ContractTypes []abi.ContractName `form:"interface"`
	OwnerAddress  *addr.Address      // `form:"owner_address"`
	MinterAddress *addr.Address      // `form:"minter_address"`

	ExcludeColumn []string // TODO: support relations

	Order string `form:"order"` // ASC, DESC

	AfterTxLT *uint64 `form:"after"`
	Limit     int     `form:"limit"`
	Count     bool    `form:"count"`
}

type AccountsRes struct {
	Total int                  `json:"total,omitempty"`
	Rows  []*core.AccountState `json:"results"`
}

type AccountRepository interface {
	FilterLabels(context.Context, *LabelsReq) (*LabelsRes, error)
	FilterAccounts(context.Context, *AccountsReq) (*AccountsRes, error)
}
