<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductMasagoRollSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Masago roll')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E11',
                'is_active' => true,
                'slug' => 'masago-roll-saumon-avocat',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon mangue',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna mango',
                        ],
                    ],
                ],
                'price' => 6.50,
                'code' => 'E12',
                'is_active' => true,
                'slug' => 'masago-roll-thon-mangue',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Shrimp tempura',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'E13',
                'is_active' => true,
                'slug' => 'masago-roll-tempura-crevette',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'masago-roll-saumon-avocat')->exists()) {
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
