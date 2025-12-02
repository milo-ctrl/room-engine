package walletfx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cast"
	"gitlab-code.v.show/bygame/room-engine/env"
	"gitlab-code.v.show/bygame/room-engine/walletfx/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type walletRpc struct {
	env *env.Base

	walletRpcClient  rpc.TradeServiceClient
	accountRpcClient rpc.AccountServiceClient
}

type getUsersCoinAccountRequest struct {
	UidArr []string
}

type getUsersCoinAccountInfoResponse struct {
	List []*userCoinAccountInfo
}

type userCoinAccountInfo struct {
	Uid  string
	Coin uint64
}

// GetUsersCoinAccount implements rpc.AccountServiceClient.
func (w *walletRpc) GetUsersCoinAccount(ctx context.Context, in *getUsersCoinAccountRequest, opts ...grpc.CallOption) (*getUsersCoinAccountInfoResponse, error) {
	response := &getUsersCoinAccountInfoResponse{}

	// 开发环境不管什么都成功
	if w.walletRpcClient == nil {
		response.List = make([]*userCoinAccountInfo, 0, len(in.UidArr))
		for _, uid := range in.UidArr {
			response.List = append(response.List, &userCoinAccountInfo{
				Uid:  uid,
				Coin: 10_0000,
			})
		}
		return response, nil
	}

	//var haveTest bool
	//for _, uid := range in.UidArr {
	//	if strings.HasPrefix(uid, "test") {
	//		haveTest = true
	//		break
	//	}
	//}
	//
	//if haveTest && w.env.Environment == env.Prod {
	//	return nil, ErrTestAccount
	//}
	//
	//// 测试环境允许test_uid
	//if haveTest {
	//	response.List = make([]*userCoinAccountInfo, 0, len(in.UidArr))
	//	for _, uid := range in.UidArr {
	//		response.List = append(response.List, &userCoinAccountInfo{
	//			Uid:  uid,
	//			Coin: 10_0000,
	//		})
	//	}
	//	return response, nil
	//}

	uidArr := make([]uint32, 0, len(in.UidArr))
	for _, uid := range in.UidArr {
		uidArr = append(uidArr, cast.ToUint32(uid))
	}

	rpcResp, err := w.accountRpcClient.GetUsersCoinAccount(ctx, &rpc.GetUsersCoinAccountRequest{
		UidArr: uidArr,
	}, opts...)
	if err != nil {
		return nil, err
	}

	for _, info := range rpcResp.List {
		response.List = append(response.List, &userCoinAccountInfo{
			Uid:  cast.ToString(info.Uid),
			Coin: info.Coin,
		})
	}
	return response, nil
}

// Deduct implements rpc.TradeServiceClient.
func (w *walletRpc) Deduct(ctx context.Context, in *rpc.TradeRequest, opts ...grpc.CallOption) (*rpc.TradeResponse, error) {
	// 开发环境不管什么都成功
	if w.walletRpcClient == nil {
		return &rpc.TradeResponse{
			Code:    200,
			Message: "success",
		}, nil
	}

	//var haveTest bool
	//for _, action := range in.Detail {
	//	if action.Uid == 0 {
	//		haveTest = true
	//		break
	//	}
	//}
	//
	//if haveTest && w.env.Environment == env.Prod {
	//	return nil, ErrTestAccount
	//}
	//
	//// 测试环境允许test_uid
	//if haveTest {
	//	return &rpc.TradeResponse{
	//		Code:    200,
	//		Message: "success",
	//	}, nil
	//}

	return w.walletRpcClient.Deduct(ctx, in, opts...)
}

// Refund implements rpc.TradeServiceClient.
func (w *walletRpc) Refund(ctx context.Context, in *rpc.RefundRequest, opts ...grpc.CallOption) (*rpc.TradeResponse, error) {
	// 开发环境不管什么都成功
	if w.walletRpcClient == nil {
		return &rpc.TradeResponse{
			Code:    200,
			Message: "success",
		}, nil
	}

	return w.walletRpcClient.Refund(ctx, in, opts...)
}

// Reward implements rpc.TradeServiceClient.
func (w *walletRpc) Reward(ctx context.Context, in *rpc.TradeRequest, opts ...grpc.CallOption) (*rpc.TradeResponse, error) {
	// 开发环境不管什么都成功
	if w.walletRpcClient == nil {
		return &rpc.TradeResponse{
			Code:    200,
			Message: "success",
		}, nil
	}

	//var haveTest bool
	//for _, action := range in.Detail {
	//	if action.Uid == 0 {
	//		haveTest = true
	//		break
	//	}
	//}
	//
	//if haveTest && w.env.Environment == env.Prod {
	//	return nil, ErrTestAccount
	//}
	//
	//// 测试环境允许test_uid
	//if haveTest {
	//	return &rpc.TradeResponse{
	//		Code:    200,
	//		Message: "success",
	//	}, nil
	//}
	return w.walletRpcClient.Reward(ctx, in, opts...)
}

func newWalletClient(params NewWalletParams) (*walletRpc, error) {
	// 非开发环境必须传WalletUrl
	if params.Env.Environment != env.Dev && params.WalletUrl == "" {
		return nil, errors.New("WalletUrl is empty")
	}
	client := walletRpc{
		env: params.Env,
	}
	if params.WalletUrl != "" {
		// 初始化grpc
		ctx, cancelFunc := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancelFunc()
		conn, err := grpc.DialContext(
			ctx,
			params.WalletUrl,
			grpc.WithBlock(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)

		if err != nil {
			//slog.Error("wallet grpc client error", "url", params.WalletUrl, "error", err)
			//panic(err)
			return nil, fmt.Errorf("wallet grpc client err:%w", err)
		}
		slog.Info("wallet grpc client init success", "url", params.WalletUrl)
		client.walletRpcClient = rpc.NewTradeServiceClient(conn)
		client.accountRpcClient = rpc.NewAccountServiceClient(conn)
	}
	return &client, nil
}

var _ rpc.TradeServiceClient = &walletRpc{}
