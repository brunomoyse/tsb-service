<?php

namespace App\GraphQL\Mutations;

use App\Models\Order;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Support\Str;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;
use Stripe\Product as StripeProduct;
use Stripe\StripeClient;

class OrderResolver
{
    private StripeClient $stripe;

    public function __construct()
    {
        $this->stripe = new StripeClient(config('stripe.secret_key'));
    }

    public function createOrder(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Order
    {
        $productIds = array_column($args['products'], 'product_id');

        try {
            // Fetch products from Stripe in a single call
            $stripeProducts = $this->stripe->products->all(['ids' => $productIds, 'expand' => ['data.prices']]);
        } catch (\Exception $e) {
            throw new \Exception('Error fetching Stripe products: '.$e->getMessage());
        }

        // Map the products to desired format
        $transformedData = collect($args['products'])->map(function ($item) use ($stripeProducts) {
            /** @var StripeProduct $matchedProduct */
            $matchedProduct = collect($stripeProducts->data)->firstWhere('id', $item['product_id']);
            $priceId = $matchedProduct->default_price;

            return [
                'price' => $priceId,
                'quantity' => $item['quantity'],
            ];
        })->toArray();

        // Set UUID for the future Order now to be able to use it in the link
        $generatedUuid = Str::uuid();
        try {
            $stripeSession = $this->stripe->checkout->sessions->create([
                'mode' => 'payment',
                'success_url' => config('services.ui.endpoint').'order/'.$generatedUuid.'/success',
                'line_items' => $transformedData,
                'payment_method_types' => ['card', 'bancontact', 'wechat_pay'],
                'payment_method_options' => [
                    'wechat_pay' => [
                        'client' => 'web',
                    ],
                    'bancontact' => [
                        'setup_future_usage' => 'none',
                    ],
                    'card' => [
                        'setup_future_usage' => 'on_session',
                    ],
                ],
            ]);
        } catch (\Exception $e) {
            throw new \Exception('Error creating Stripe payment session: '.$e->getMessage());
        }

        // Creating order in database
        /** @var Order $order */
        $order = Order::query()->create([
            'id' => $generatedUuid,
            'payment_mode' => 'ONLINE',
            'status' => 'PENDING',
            'stripe_session_id' => $stripeSession->id,
            'stripe_checkout_url' => $stripeSession->url,
            // @todo Update by $context->user()->id in production
            'user_id' => '99d9e2ef-2853-4b7b-87bd-4a1540fed7b6',
        ]);

        // fill the pivot table
        $order->products()->attach($args['products']);

        return $order->load('products');
    }

    public function updateOrderStatus(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Order
    {
        /** @var Order $order */
        $order = Order::query()->findOrFail($args['id']);

        $order->update([
            'status' => $args['status'],
        ]);

        return $order;
    }
}
