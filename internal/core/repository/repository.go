package repository

import (
	"github.com/stepandra/anton/internal/core"
	"github.com/stepandra/anton/internal/core/aggregate"
	"github.com/stepandra/anton/internal/core/aggregate/history"
	"github.com/stepandra/anton/internal/core/filter"
)

type Block interface {
	core.BlockRepository
	filter.BlockRepository
}

type Account interface {
	core.AccountRepository
	filter.AccountRepository
	aggregate.AccountRepository
	history.AccountRepository
}

type Transaction interface {
	core.TransactionRepository
	filter.TransactionRepository
	history.TransactionRepository
}

type Message interface {
	core.MessageRepository
	filter.MessageRepository
	aggregate.MessageRepository
	history.MessageRepository
}

type Contract interface {
	core.ContractRepository
}
