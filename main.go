package main

import (
	"fmt"
	"path/filepath"

	pb "github.com/decred/dcrwallet/rpc/walletrpc"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/decred/dcrd/dcrutil"
)

const (
	requiredConfirmations = 1
	accountNumber         = 0 // default account
)

var certificateFile = filepath.Join(dcrutil.AppDataDir("dcrwallet", false), "rpc.cert")

func main() {
	creds, err := credentials.NewClientTLSFromFile(certificateFile, "localhost")
	if err != nil {
		fmt.Println(err)
		return
	}
	conn, err := grpc.Dial("localhost:19111", grpc.WithTransportCredentials(creds))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer conn.Close()
	walletService := pb.NewWalletServiceClient(conn)

	ctx := context.Background()

	balanceRequest := &pb.BalanceRequest{
		AccountNumber:         0,
		RequiredConfirmations: requiredConfirmations,
	}
	balanceResponse, err := walletService.Balance(ctx, balanceRequest)
	if err != nil {
		fmt.Println(err)
		return
	}

	spendableBal := dcrutil.Amount(balanceResponse.Spendable)
	fmt.Println("Spendable balance:", spendableBal)

	addressRequest := &pb.NextAddressRequest{
		Account:   0,
		Kind:      pb.NextAddressRequest_BIP0044_INTERNAL,
		GapPolicy: pb.NextAddressRequest_GAP_POLICY_WRAP,
	}
	addressResponse, err := walletService.NextAddress(ctx, addressRequest)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Send some DCR to:", addressResponse.Address)

	notifiationClient, err := walletService.TransactionNotifications(ctx, &pb.TransactionNotificationsRequest{})
	if err != nil {
		fmt.Println(err)
		return
	}

	ticketPrice, err := getTicketPrice(ctx, walletService)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Printf("Ticket Price: %s\n", ticketPrice)

	fmt.Println("Listening for block notifcations")
	for {
		notificationResponse, err := notifiationClient.Recv()
		if err != nil {
			fmt.Println(err)
			if err == context.Canceled {
				return
			}

			continue
		}

		ticketPrice, err := getTicketPrice(ctx, walletService)
		if err != nil {
			fmt.Println(err)
			return
		}

		numAttachedBlocks := len(notificationResponse.AttachedBlocks)
		fmt.Printf("%d block(s) attached, Ticket Price: %s\n", numAttachedBlocks, ticketPrice)

		err = purchaseTicket(ctx, walletService)
		if err != nil {
			fmt.Println(err)
			return
		}
	}

	select {}
}

func getTicketPrice(ctx context.Context, walletService pb.WalletServiceClient) (dcrutil.Amount, error) {
	ticketPriceResponse, err := walletService.TicketPrice(ctx, &pb.TicketPriceRequest{})
	if err != nil {
		return 0, err
	}

	return dcrutil.Amount(ticketPriceResponse.TicketPrice), nil
}

func purchaseTicket(ctx context.Context, walletService pb.WalletServiceClient) error {
	pruchaseTicketRequest := &pb.PurchaseTicketsRequest{
		NumTickets:            1,
		Account:               0,
		RequiredConfirmations: requiredConfirmations,
		Passphrase:            []byte("c"),
	}

	pruchaseTicketResponse, err := walletService.PurchaseTickets(ctx, pruchaseTicketRequest)
	if err != nil {
		return err
	}

	ticketHash := pruchaseTicketResponse.TicketHashes[0][0]

	fmt.Printf("Purchased ticket: %s\n", ticketHash)
	return nil
}
