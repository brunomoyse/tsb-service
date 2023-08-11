<?php

namespace App\GraphQL\Mutations;

use App\Models\Product;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Support\Str;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;
use Stripe\StripeClient;

class ProductResolver
{
    private StripeClient $stripe;

    public function __construct()
    {
        $this->stripe = new StripeClient(config('stripe.secret_key'));
    }

    public function createProduct($rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo)
    {
        try {
            $newUuid = Str::uuid();

            $frenchData = current(array_filter($args['productTranslations']['create'], function ($item) {
                return $item['language'] === 'FR';
            }));

            $stripeProduct = $this->stripe->products->create([
                'id' => $newUuid,
                'name' => $frenchData['name'],
                'active' => $args['isActive'],
                'description' => $frenchData['description'],
                'default_price_data' => [
                    'currency' => 'eur',
                    'unit_amount_decimal' => $args['price'] * 100,
                    'tax_behavior' => 'inclusive',
                ],
            ]);
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

    public function updateProduct($rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo)
    {
        /** @var Product $product */
        $product = Product::query()->findOrFail($args['id']);

        if (isset($args['isActive'])) {
            try {
                $this->stripe->products->update($args['id'], [
                    'active' => $args['isActive'],
                ]);
            } catch (\Exception $e) {
                throw new \Exception('Error updating Stripe product: '.$e->getMessage());
            }
        }

        // If the price has been changed
        if (isset($args['price']) && ($args['price'] !== $product->price)) {
            // I create a new price
            try {
                $newPrice = $this->stripe->prices->create([
                    'product' => $product->id,
                    'currency' => 'eur',
                    'unit_amount_decimal' => $args['price'] * 100,
                    'tax_behavior' => 'inclusive',
                ]);
            } catch (\Exception $e) {
                throw new \Exception('Error creating Stripe price: '.$e->getMessage());
            }

            // I apply the new price to the product
            try {
                $this->stripe->products->update($product->id, [
                    'default_price' => $newPrice->id,
                ]);
            } catch (\Exception $e) {
                throw new \Exception('Error applying new Stripe price to Stripe product: '.$e->getMessage());
            }
        }

        try {
            $product->update($args);

            if (isset($args['productTags']['connect'])) {
                $product->productTags()->sync($args['productTags']['connect']);
            }

            return $product->load('productTranslations', 'productTags');
        } catch (\Exception $e) {
            throw new \Exception('Error updating product: '.$e->getMessage());
        }

    }
}
