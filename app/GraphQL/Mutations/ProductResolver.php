<?php

namespace App\GraphQL\Mutations;

use App\Models\Product;
use Exception;
use GraphQL\Type\Definition\ResolveInfo;
use Illuminate\Http\JsonResponse;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Storage;
use Nuwave\Lighthouse\Support\Contracts\GraphQLContext;

class ProductResolver
{
    public function createProduct(null $rootValue, array $args, GraphQLContext $context, ResolveInfo $resolveInfo): Product|JsonResponse
    {
        $product = null;
        $error = null;
        try {
            DB::transaction(function () use ($args, &$product) {
                /** @var Product $product */
                $product = Product::query()->create([
                    'code' => $args['code'],
                    'price' => $args['price'],
                    'is_active' => $args['is_active'] ?? true,
                ]);

                // Create related translations
                $product->productTranslations()->createMany($args['productTranslations']['create']);

                // Sync tags
                $product->productTags()->sync($args['productTags']['connect']);
            });
        } catch (Exception $e) {
            $error = 'Product creation failed: ' . $e->getMessage();
        }

        // Check if product was created and return it with loaded relationships or return the error
        if ($product) {
            return $product->load('productTranslations', 'productTags');
        } else {
            // You might want to use your own error handling/response mechanism here
            return response()->json(['error' => $error], 500);
        }
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
                            'locale', '=', $translation['locale']
                        ],
                        [
                            'product_id', '=', $product->id
                        ]
                    ])->update($translation);
                }
            }

            if (isset($args['productTags']['connect'])) {
                $product->productTags()->sync($args['productTags']['connect']);
            }

            return $product->load('productTranslations', 'productTags');
        } catch (\Exception $e) {
            throw new \Exception('Error updating product: '.$e->getMessage());
        }

    }

    private function handleFileUpload($file): void
    {
        $fileName = 'product_images/' . uniqid() . '.' . $file->getClientOriginalExtension();

        Storage::disk('public')->put($fileName, file_get_contents($file->getRealPath()));
    }
}
