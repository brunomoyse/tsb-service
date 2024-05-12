<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductChirashiSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Chirashi')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tranches (ou tartare) de saumon avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Slices (or tartare) of salmon and avocado',
                        ],
                    ],
                ],
                'price' => 14.80,
                'code' => 'H1',
                'is_active' => true,
                'slug' => 'chirashi-saumon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tranches (ou tartare) de thon avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Slices (or tartare) of tuna and avocado',
                        ],
                    ],
                ],
                'price' => 15.80,
                'code' => 'H2',
                'is_active' => true,
                'slug' => 'chirashi-thon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Assortiment',
                            'description' => 'Saumon, thon, dorade, crevette, oeufs de saumon, avocat, radis japonais',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Mix',
                            'description' => 'Salmon, tuna, sea bream, shrimp, salmon eggs, avocado, Japanese radish.',
                        ],
                    ],
                ],
                'price' => 17.80,
                'code' => 'H4',
                'is_active' => true,
                'slug' => 'chirashi-assortiment',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'chirashi-saumon-avocat')->exists()) {
            return;
        }

        foreach ($products as $product) {
            try {
                /* @var Product $productItem */
                $productItem = Product::query()->create([
                    'price' => $product['price'],
                    'is_active' => true,
                    'slug' => $product['slug'],
                    'code' => $product['code'] ?? null,
                ]);

                $productItem->productTranslations()->createMany($product['productTranslations']['create']);
                $productItem->productCategories()->sync($product['productCategories']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
