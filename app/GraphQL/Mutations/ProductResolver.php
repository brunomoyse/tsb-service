<?php

namespace App\GraphQL\Mutations;

use App\Models\Product;
use Exception;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Http\JsonResponse;
use Illuminate\Support\Arr;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Str;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class ProductResolver
{
    public function createProduct(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product|JsonResponse
    {
        return DB::transaction(function () use ($args) {
            $frLocaleItem = Arr::first($args['productTranslations']['create'], function ($localeItem) {
                return $localeItem['locale'] === 'FR';
            });

            /** @var Product $product */
            $product = Product::query()->create([
                'code' => $args['code'],
                'price' => $args['price'],
                'is_active' => $args['is_active'] ?? true,
                'slug' => Str::slug($frLocaleItem['name']),
            ]);

            // Create related translations
            $product->productTranslations()->createMany($args['productTranslations']['create']);

            // Sync categories
            $product->productCategories()->sync($args['productCategories']['connect']);

            return $product->load('productTranslations', 'productCategories');
        });
    }

    public function updateProduct(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product
    {
        /** @var Product $product */
        $product = Product::query()->findOrFail($args['id']);

        try {
            $product->update($args);

            if (isset($args['productTranslations']['update'])) {
                foreach ($args['productTranslations']['update'] as $translation) {
                    $product->productTranslations()->where([
                        [
                            'locale', '=', $translation['locale'],
                        ],
                        [
                            'product_id', '=', $product->id,
                        ],
                    ])->update($translation);
                }
            }

            if (isset($args['productCategories']['connect'])) {
                $product->productCategories()->sync($args['productCategories']['connect']);
            }

            return $product->load('productTranslations', 'productCategories');
        } catch (Exception $e) {
            throw new Exception('Error updating product: '.$e->getMessage());
        }

    }
}
