<?php

namespace App\GraphQL\Mutations;

use App\Models\Product;
use GraphQL\Type\Definition\ResolveInfo;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;
use Stripe\StripeClient;
use Stripe\Product as StripeProduct;
use Stripe\Stripe;

class ProductResolver
{
    private StripeClient $stripe;

    public function __construct()
    {
        $this->stripe = new StripeClient(config('stripe.secret_key'));
    }

        try {
            $newUuid = Str::uuid();

            $frenchData = current(array_filter($args['productTranslations']['create'], function($item) {
                return $item['language'] === 'FR';
            }));

            $stripeProduct = StripeProduct::create([
                'id' => $newUuid,
                'name' => $frenchData['name'],
                'active' => $args['is_active'],
                'description' => $frenchData['description'],
                'default_price_data' => [
                    'currency' => 'eur',
                    'unit_amount_decimal' => $args['price'] * 100,
                    'tax_behavior' => 'inclusive'
                ]
            ], ['expand' => ['default_price']]);
        } catch (\Exception $e) {
            throw new \Exception('Erreur lors de la création du produit Stripe: '.$e->getMessage());
        }

        try {
            // I fill the price directly from the request to avoid making a new request to get the price
            // since the price is a separate object in Stripe
            $product = Product::query()->create([
                'id' => $stripeProduct->id,
                'price' => $args['price'],
                'is_active' => $stripeProduct->active,
            ]);

            $product->productTranslations()->createMany($args['productTranslations']['create']);
            $product->productTags()->sync($args['productTags']['connect']);

            return $product->load('productTranslations', 'productTags');
        } catch (\Exception $e) {
            throw new \Exception('Erreur lors de la création du produit: '.$e->getMessage());
        }
    }
}
