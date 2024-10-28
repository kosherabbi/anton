package app

import (
	"github.com/stepandra/anton/internal/core"
	"github.com/stepandra/anton/internal/core/repository"
)

type RescanConfig struct {
	ContractRepo core.ContractRepository
	RescanRepo   core.RescanRepository
	BlockRepo    repository.Block
	AccountRepo  repository.Account
	MessageRepo  repository.Message

	Parser ParserService

	Workers int

	SelectLimit int
}

type RescanService interface {
	Start() error
	Stop()
}
