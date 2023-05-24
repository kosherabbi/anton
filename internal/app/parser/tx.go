package parser

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/tvm/cell"

	"github.com/tonindexer/anton/internal/app"
	"github.com/tonindexer/anton/internal/core"
)

func (s *Service) parseDirectedMessage(ctx context.Context, acc *core.AccountState, msg *core.Message) error {
	if acc == nil {
		return errors.Wrap(app.ErrImpossibleParsing, "no account data")
	}
	if len(acc.Types) == 0 {
		return errors.Wrap(app.ErrImpossibleParsing, "no interfaces")
	}

	op, err := s.contractRepo.GetOperationByID(ctx, acc.Types, acc.Address == msg.SrcAddress, msg.OperationID)
	if errors.Is(err, core.ErrNotFound) {
		return errors.Wrap(app.ErrImpossibleParsing, "unknown operation")
	}
	if err != nil {
		return errors.Wrap(err, "get contract operations")
	}
	msg.OperationName = op.OperationName

	// set src and dst contract types
	if acc.Address == msg.SrcAddress {
		msg.SrcContract = op.ContractName
	} else {
		msg.DstContract = op.ContractName
	}

	msg.MinterAddress = acc.MinterAddress

	msgParsed, err := op.Schema.New()
	if err != nil {
		return errors.Wrapf(err, "creating struct from %s/%s schema", op.ContractName, op.OperationName)
	}

	payloadCell, err := cell.FromBOC(msg.Body)
	if err != nil {
		return errors.Wrap(err, "msg body from boc")
	}
	payloadSlice := payloadCell.BeginParse()

	if err = tlb.LoadFromCell(msgParsed, payloadSlice); err != nil {
		return errors.Wrap(err, "load from cell")
	}

	msg.DataJSON, err = json.Marshal(msgParsed)
	if err != nil {
		return errors.Wrap(err, "json marshal parsed payload")
	}

	return nil
}

func (s *Service) ParseMessagePayload(ctx context.Context, src, dst *core.AccountState, msg *core.Message) (*core.Message, error) {
	var err = app.ErrImpossibleParsing // save message parsing error to a database to look at it later

	// you can parse separately incoming messages to known contracts and outgoing message from them

	if len(msg.Body) == 0 {
		return nil, errors.Wrap(app.ErrImpossibleParsing, "no message body")
	}

	errIn := s.parseDirectedMessage(ctx, dst, msg)
	if errIn != nil && !errors.Is(errIn, app.ErrImpossibleParsing) {
		log.Warn().Err(errIn).
			Uint64("src_tx_lt", msg.SrcTxLT).
			Str("dst_addr", dst.Address.Base64()).
			Uint32("op_id", msg.OperationID).Msgf("parse dst %v message", dst.Types)
		err = errors.Wrap(errIn, "incoming")
	}
	if errIn == nil {
		return msg, nil
	}

	errOut := s.parseDirectedMessage(ctx, src, msg)
	if errOut != nil && !errors.Is(errOut, app.ErrImpossibleParsing) {
		log.Warn().Err(errOut).
			Uint64("src_tx_lt", msg.SrcTxLT).
			Str("src_addr", src.Address.Base64()).
			Uint32("op_id", msg.OperationID).Msgf("parse src %v message", src.Types)
		err = errors.Wrap(errOut, "outgoing")
	}
	if errOut == nil {
		return msg, nil
	}

	return msg, err
}
