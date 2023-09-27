<?php

namespace App\Http\Controllers;

use App\Models\Order;
use Illuminate\Http\Request;
use Mollie\Api\MollieApiClient;

class MollieController extends Controller
{
    private MollieApiClient $mollie;

    public function __construct()
    {
        $this->mollie = new MollieApiClient();
        $this->mollie->setApiKey(config('mollie.api_key'));
    }

    public function updateOrderStatus(Request $request): void
    {
        $request->validate([
            'id' => 'required|string',
            'status' => 'required|string',
        ]);

        try {
            /*
             * Retrieve the payment's current state.
             */
            $payment = $this->mollie->payments->get($request->id);
            $order = Order::query()->where('mollie_payment_id', $request->id)->firstOrFail();

            /*
             * Update the order in the database.
             */
            $order->update([
                'status' => strtoupper($payment->status),
            ]);

            if ($payment->isPaid() && ! $payment->hasRefunds() && ! $payment->hasChargebacks()) {
                /*
                 * The payment is paid and isn't refunded or charged back.
                 * At this point you'd probably want to start the process of delivering the product to the customer.
                 */
            }
        } catch (\Mollie\Api\Exceptions\ApiException $e) {
            echo 'API call failed: '.htmlspecialchars($e->getMessage());
        }
    }
}
