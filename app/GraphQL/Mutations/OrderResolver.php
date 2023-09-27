<?php

namespace App\GraphQL\Mutations;

use App\Models\Order;
use App\Models\Product;
use App\Models\User;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Support\Str;
use Mollie\Api\Exceptions\ApiException;
use Mollie\Api\MollieApiClient;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class OrderResolver
{
    private MollieApiClient $mollie;

    /**
     * @throws ApiException
     */
    public function __construct()
    {
        $this->mollie = new MollieApiClient();
        $this->mollie->setApiKey(config('mollie.api_key'));
    }

    /**
     * @throws ApiException
     */
    public function createOrder(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Order
    {
        $products = $args['products'];
        $userId = $context->user()->id ?? '99d9e2ef-2853-4b7b-87bd-4a1540fed7b6';

        // calculate total amount of the order
        $totalAmount = 0;
        foreach ($products as $item) {
            /** @var Product $model */
            $model = Product::query()->findOrFail($item['product_id']);
            $item['price'] = $model->price;
            $totalAmount += $item['price'] * $item['quantity'];
        }
        $totalAmount = number_format($totalAmount, 2);

        // Generate a uuid for the order
        $generatedUuid = Str::uuid();

        $description = 'Client: '.$this->getUsername($userId).' | '.'Commande n°'.$generatedUuid;

        $payment = $this->mollie->payments->create([
            'amount' => [
                'currency' => 'EUR',
                'value' => $totalAmount,
            ],
            'description' => $description,
            'redirectUrl' => config('services.ui.endpoint').'order/'.$generatedUuid.'/success',
            'cancelUrl' => config('services.ui.endpoint').'order/'.$generatedUuid.'/cancel',
            //'webhookUrl'  => config('services.ui.endpoint').'mollie/webhook',
        ]);

        /** @var Order $order */
        $order = Order::query()->create([
            'id' => $generatedUuid,
            'payment_mode' => 'ONLINE',
            'status' => 'OPEN',
            'mollie_payment_id' => $payment->id,
            'mollie_payment_url' => $payment->getCheckoutUrl(),
            // @todo Update by $context->user()->id in production
            'user_id' => '99d9e2ef-2853-4b7b-87bd-4a1540fed7b6',
        ]);

        // fill the pivot table
        $order->products()->attach($args['products']);

        return $order->load('products');
    }

    private function getUsername(string $userId): string
    {
        $user = User::query()->findOrFail($userId);

        return $user->name;
    }
}
