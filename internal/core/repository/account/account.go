package account

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"

	"github.com/uptrace/bun"
	"github.com/uptrace/go-clickhouse/ch"

	"github.com/stepandra/anton/abi"
	"github.com/tonindexer/anton/addr"
	"github.com/tonindexer/anton/internal/core"
	"github.com/tonindexer/anton/internal/core/repository"
)

var _ repository.Account = (*Repository)(nil)

type Repository struct {
	ch *ch.DB
	pg *bun.DB
}

func NewRepository(ck *ch.DB, pg *bun.DB) *Repository {
	return &Repository{ch: ck, pg: pg}
}

func createIndexes(ctx context.Context, pgDB *bun.DB) error {
	// account data

	_, err := pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Using("HASH").
		Column("owner_address").
		Where("owner_address IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address state owner pg create index")
	}

	_, err = pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Using("HASH").
		Column("minter_address").
		Where("minter_address IS NOT NULL").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address state minter pg create index")
	}

	_, err = pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Using("GIN").
		Column("types").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state contract types pg create index")
	}

	// account state

	_, err = pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Unique().
		Column("address", "workchain", "shard", "block_seq_no").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address state address in block pg create unique index")
	}

	_, err = pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Using("HASH").
		Column("address").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address state address pg create index")
	}

	_, err = pgDB.NewCreateIndex().
		Model(&core.AccountState{}).
		Using("BTREE").
		Column("last_tx_lt").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state last_tx_lt pg create index")
	}

	// latest account state

	_, err = pgDB.NewCreateIndex().
		Model(&core.LatestAccountState{}).
		Using("BTREE").
		Column("last_tx_lt").
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "latest account state last_tx_lt pg create index")
	}

	return nil
}

func CreateTables(ctx context.Context, chDB *ch.DB, pgDB *bun.DB) error {
	_, err := pgDB.ExecContext(ctx, "CREATE TYPE account_status AS ENUM (?, ?, ?, ?)",
		core.Uninit, core.Active, core.Frozen, core.NonExist)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return errors.Wrap(err, "account status pg create enum")
	}

	_, err = pgDB.ExecContext(ctx, "CREATE TYPE label_category AS ENUM (?, ?)",
		core.CentralizedExchange, core.Scam)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return errors.Wrap(err, "address label category pg create enum")
	}

	_, err = chDB.NewCreateTable().
		IfNotExists().
		Engine("ReplacingMergeTree").
		Model(&core.AddressLabel{}).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address label ch create table")
	}

	_, err = pgDB.NewCreateTable().
		Model(&core.AddressLabel{}).
		IfNotExists().
		WithForeignKeys().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "address label pg create table")
	}

	_, err = chDB.NewCreateTable().
		IfNotExists().
		Engine("EmbeddedRocksDB PRIMARY KEY code_hash").
		Model(&core.AccountStateCode{}).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state code ch create table")
	}
	_, err = chDB.NewCreateTable().
		IfNotExists().
		Engine("EmbeddedRocksDB PRIMARY KEY data_hash").
		Model(&core.AccountStateData{}).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state data ch create table")
	}

	_, err = chDB.NewCreateTable().
		IfNotExists().
		Engine("ReplacingMergeTree").
		Model(&core.AccountState{}).
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state ch create table")
	}

	_, err = pgDB.NewCreateTable().
		Model(&core.AccountState{}).
		IfNotExists().
		// WithForeignKeys().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "account state pg create table")
	}

	_, err = pgDB.NewCreateTable().
		Model(&core.LatestAccountState{}).
		IfNotExists().
		WithForeignKeys().
		Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "latest account state pg create table")
	}

	return createIndexes(ctx, pgDB)
}

func (r *Repository) AddAddressLabel(ctx context.Context, label *core.AddressLabel) error {
	_, err := r.pg.NewInsert().Model(label).Exec(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key value violates unique constraint") {
			return errors.Wrap(core.ErrAlreadyExists, "address is already labeled")
		}
		return errors.Wrap(err, "pg insert label")
	}
	_, err = r.ch.NewInsert().Model(label).Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "ch insert label")
	}
	return nil
}

func (r *Repository) GetAddressLabel(ctx context.Context, a addr.Address) (*core.AddressLabel, error) {
	var label = core.AddressLabel{Address: a}

	err := r.pg.NewSelect().Model(&label).WherePK().Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	return &label, nil
}

func (r *Repository) AddAccountStates(ctx context.Context, tx bun.Tx, accounts []*core.AccountState) error {
	if len(accounts) == 0 {
		return nil
	}

	for _, a := range accounts {
		for _, executions := range a.ExecutedGetMethods {
			sort.Slice(executions, func(i, j int) bool { return executions[i].Name < executions[j].Name })
		}
	}

	var (
		codeKV []*core.AccountStateCode
		dataKV []*core.AccountStateData
	)
	for _, a := range accounts {
		codeKV = append(codeKV, &core.AccountStateCode{CodeHash: a.CodeHash, Code: a.Code})
		dataKV = append(dataKV, &core.AccountStateData{DataHash: a.DataHash, Data: a.Data})
		a.Code, a.Data = nil, nil
	}

	if _, err := r.ch.NewInsert().Model(&codeKV).Exec(ctx); err != nil {
		return errors.Wrapf(err, "write code to key-value store")
	}
	if _, err := r.ch.NewInsert().Model(&dataKV).Exec(ctx); err != nil {
		return errors.Wrapf(err, "write data to key-value store")
	}

	_, err := tx.NewInsert().Model(&accounts).Exec(ctx)
	if err != nil {
		return errors.Wrapf(err, "cannot insert new account states")
	}

	addrTxLT := make(map[addr.Address]uint64)
	for _, a := range accounts {
		if addrTxLT[a.Address] < a.LastTxLT {
			addrTxLT[a.Address] = a.LastTxLT
		}
	}

	for a, lt := range addrTxLT {
		_, err := tx.NewInsert().
			Model(&core.LatestAccountState{
				Address:  a,
				LastTxLT: lt,
			}).
			On("CONFLICT (address) DO UPDATE").
			Where("latest_account_state.last_tx_lt < ?", lt).
			Set("last_tx_lt = EXCLUDED.last_tx_lt").
			Exec(ctx)
		if err != nil {
			return errors.Wrapf(err, "cannot set latest state for %s", &a)
		}
	}

	_, err = r.ch.NewInsert().Model(&accounts).Exec(ctx)
	if err != nil {
		return err
	}

	return nil
}

func logAccountStateDataUpdate(acc *core.AccountState) {
	types, _ := json.Marshal(acc.Types)                   //nolint:errchkjson // no need
	getMethods, _ := json.Marshal(acc.ExecutedGetMethods) //nolint:errchkjson // no need

	log.Debug().
		Str("address", acc.Address.Base64()).
		Uint64("last_tx_lt", acc.LastTxLT).
		RawJSON("types", types).
		RawJSON("executed_get_methods", getMethods).
		Msg("updating account state data")
}

func (r *Repository) UpdateAccountStates(ctx context.Context, accounts []*core.AccountState) error {
	if len(accounts) == 0 {
		return nil
	}

	for _, a := range accounts {
		for _, executions := range a.ExecutedGetMethods {
			sort.Slice(executions, func(i, j int) bool { return executions[i].Name < executions[j].Name })
		}

		logAccountStateDataUpdate(a)

		_, err := r.pg.NewUpdate().Model(a).
			Set("types = ?types").
			Set("owner_address = ?owner_address").
			Set("minter_address = ?minter_address").
			Set("fake = ?fake").
			Set("executed_get_methods = ?executed_get_methods").
			Set("content_uri = ?content_uri").
			Set("content_name = ?content_name").
			Set("content_description = ?content_description").
			Set("content_image = ?content_image").
			Set("content_image_data = ?content_image_data").
			Set("jetton_balance = ?jetton_balance").
			WherePK().
			Exec(ctx)
		if err != nil {
			return errors.Wrapf(err, "cannot update %s acc state data", a.Address.String())
		}
	}

	_, err := r.ch.NewInsert().Model(&accounts).Exec(ctx)
	if err != nil {
		return err
	}

	return nil
}

func (r *Repository) MatchStatesByInterfaceDesc(ctx context.Context,
	contractName abi.ContractName,
	addresses []*addr.Address,
	codeHash []byte,
	getMethodHashes []int32,
	afterAddress *addr.Address,
	afterTxLt uint64,
	limit int,
) ([]*core.AccountStateID, error) {
	var ids []*core.AccountStateID

	q := r.ch.NewSelect().Model((*core.AccountState)(nil)).
		ColumnExpr("DISTINCT address, last_tx_lt").
		WhereGroup(" AND ", func(q *ch.SelectQuery) *ch.SelectQuery {
			if contractName != "" {
				q = q.WhereOr("hasAny(types, [?])", string(contractName))
			}
			if len(addresses) > 0 {
				q = q.WhereOr("address IN ?", ch.In(addresses))
			}
			if len(codeHash) > 0 {
				q = q.WhereOr("code_hash = ?", codeHash)
			}
			if len(addresses) == 0 && len(codeHash) == 0 && len(getMethodHashes) > 0 {
				// match by get-method hashes only if addresses and code_hash are not set
				q = q.WhereOr("hasAll(get_method_hashes, ?)", ch.Array(getMethodHashes))
			}
			return q
		})
	if afterAddress != nil && afterTxLt != 0 {
		q = q.Where("(address, last_tx_lt) > (?, ?)", afterAddress, afterTxLt)
	}
	err := q.
		OrderExpr("address ASC, last_tx_lt ASC").
		Limit(limit).
		Scan(ctx, &ids)
	if err != nil {
		return nil, err
	}

	return ids, nil
}

func (r *Repository) GetAllAccountInterfaces(ctx context.Context, a addr.Address) (map[uint64][]abi.ContractName, error) {
	var ret []struct {
		ChangeTxLT  int64
		ChangeTypes []abi.ContractName `ch:"type:Array(String)"`
	}

	minTxLtSubQ := r.ch.NewSelect().Model((*core.AccountState)(nil)).
		ColumnExpr("min(last_tx_lt)").
		Where("address = ?", &a)

	err := r.ch.NewSelect().
		TableExpr("(?) AS sq", r.ch.NewSelect().Model((*core.AccountState)(nil)).
			ColumnExpr("last_tx_lt AS change_tx_lt").
			ColumnExpr("types AS change_types").
			Where("address = ? AND last_tx_lt = (?)", &a, minTxLtSubQ).
			UnionAll(
				r.ch.NewSelect().
					TableExpr("(?) AS diff",
						r.ch.NewSelect().Model((*core.AccountState)(nil)).
							ColumnExpr("last_tx_lt AS tx_lt").
							ColumnExpr("types").
							ColumnExpr("leadInFrame(last_tx_lt) OVER (ORDER BY last_tx_lt ASC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS next_tx_lt").
							ColumnExpr("leadInFrame(types)      OVER (ORDER BY last_tx_lt ASC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS next_types").
							Where("address = ?", &a).
							Order("tx_lt ASC")).
					ColumnExpr("if(next_tx_lt = 0, tx_lt, next_tx_lt) AS change_tx_lt").
					ColumnExpr("if(next_tx_lt = 0, types, next_types) AS change_types").
					Where(`
						(NOT (hasAll(types, next_types) AND hasAll(types, next_types))) OR
						(length(types) = 0 AND length(next_types) != 0) OR
						(length(types) != 0 AND length(next_types) = 0) OR
						next_tx_lt = 0`).
					Order("change_tx_lt ASC"))).
		Order("change_tx_lt ASC").
		Scan(ctx, &ret)
	if err != nil {
		return nil, err
	}

	var (
		lastInterfaces *[]abi.ContractName
		res            = map[uint64][]abi.ContractName{}
	)
	for it := range ret {
		if lastInterfaces != nil && reflect.DeepEqual(ret[it].ChangeTypes, *lastInterfaces) {
			continue
		}
		res[uint64(ret[it].ChangeTxLT)] = ret[it].ChangeTypes
		lastInterfaces = &ret[it].ChangeTypes
	}

	return res, nil
}

func (r *Repository) GetAllAccountStates(ctx context.Context, a addr.Address, beforeTxLT uint64, limit int) ([]*core.AccountState, error) {
	var ret []struct {
		ChangeTxLT     int64
		ChangeCodeHash []byte `ch:"type:String"`
		ChangeDataHash []byte `ch:"type:String"`
	}

	minTxLtSubQ := r.ch.NewSelect().Model((*core.AccountState)(nil)).
		ColumnExpr("min(last_tx_lt)").
		Where("address = ?", &a).
		Where("length(code_hash) > 0").
		Where("length(data_hash) > 0")

	err := r.ch.NewSelect().
		TableExpr("(?) AS sq", r.ch.NewSelect().Model((*core.AccountState)(nil)).
			ColumnExpr("last_tx_lt AS change_tx_lt").
			ColumnExpr("code_hash AS change_code_hash").
			ColumnExpr("data_hash AS change_data_hash").
			Where("address = ? AND last_tx_lt = (?)", &a, minTxLtSubQ).
			UnionAll(
				r.ch.NewSelect().
					TableExpr("(?) AS diff",
						r.ch.NewSelect().Model((*core.AccountState)(nil)).
							ColumnExpr("last_tx_lt AS tx_lt").
							ColumnExpr("code_hash").
							ColumnExpr("data_hash").
							ColumnExpr("leadInFrame(last_tx_lt) OVER (ORDER BY last_tx_lt ASC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS next_tx_lt").
							ColumnExpr("leadInFrame(code_hash)  OVER (ORDER BY last_tx_lt ASC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS next_code_hash").
							ColumnExpr("leadInFrame(data_hash)  OVER (ORDER BY last_tx_lt ASC ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING) AS next_data_hash").
							Where("address = ?", &a).
							Where("length(code_hash) > 0").
							Where("length(data_hash) > 0").
							Order("tx_lt ASC")).
					ColumnExpr("if(next_tx_lt = 0, tx_lt, next_tx_lt) AS change_tx_lt").
					ColumnExpr("if(next_tx_lt = 0, code_hash, next_code_hash) AS change_code_hash").
					ColumnExpr("if(next_tx_lt = 0, data_hash, next_data_hash) AS change_data_hash").
					Where(`
						code_hash != next_code_hash OR
						data_hash != next_data_hash OR
						next_tx_lt = 0`).
					Order("change_tx_lt ASC"))).
		Order("change_tx_lt ASC").
		Scan(ctx, &ret)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, errors.Wrapf(core.ErrNotFound, "no account states for %s address", a.Base64())
	}

	var (
		lastCodeHash, lastDataHash []byte
		lts                        []uint64
	)
	for it := range ret {
		if lastCodeHash != nil && bytes.Equal(ret[it].ChangeCodeHash, lastCodeHash) && bytes.Equal(ret[it].ChangeDataHash, lastDataHash) {
			continue
		}
		lastCodeHash, lastDataHash = ret[it].ChangeCodeHash, ret[it].ChangeDataHash
		lts = append(lts, uint64(ret[it].ChangeTxLT))
	}

	if len(lts) > limit {
		var found bool
		for it := range lts {
			if !found {
				found = lts[it] >= beforeTxLT
				continue
			}
			if it > limit {
				lts = lts[it-limit : it]
			} else {
				lts = lts[0:limit]
			}
			break
		}
		if !found {
			lts = lts[len(lts)-limit:]
		}
	}

	var states []*core.AccountState
	err = r.pg.NewSelect().Model(&states).
		Where("address = ?", &a).
		Where("last_tx_lt IN (?)", bun.In(lts)).
		Order("last_tx_lt ASC").
		Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "select states by lts")
	}

	if err := r.getCodeData(ctx, states, false, false); err != nil {
		return nil, errors.Wrap(err, "get code and data")
	}

	return states, nil
}
