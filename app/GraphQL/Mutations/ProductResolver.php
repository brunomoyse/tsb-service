<?php

namespace App\GraphQL\Mutations;

use App\Models\Product;
use GraphQL\Type\Definition\ResolveInfo;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class ProductResolver
{
    public function createProduct(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product
    {
        try {
            /** @var Product $product */
            $product = Product::query()->create([
                'price' => $args['price'],
                'is_active' => $args['is_active'] ?? true,
            ]);

            $product->productTranslations()->createMany($args['productTranslations']['create']);
            $product->productTags()->sync($args['productTags']['connect']);

            return $product->load('productTranslations', 'productTags');
        } catch (\Exception $e) {
            throw new \Exception('Error creating product: '.$e->getMessage());
        }
    }

    public function updateProduct(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product
    {
        /** @var Product $product */
        $product = Product::query()->findOrFail($args['id']);

        try {
            $product->update($args);

            // @todo update translations
            if (isset($args['productTranslations']['create'])) {
                //$product->productTranslations()->createMany($args['productTranslations']['create']);
            }

            if (isset($args['productTags']['connect'])) {
                $product->productTags()->sync($args['productTags']['connect']);
            }

            return $product->load('productTranslations', 'productTags');
        } catch (\Exception $e) {
            throw new \Exception('Error updating product: '.$e->getMessage());
        }

    }
}
