<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductTemakiSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Temaki')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon avocat cocombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.50,
                'code' => 'F1',
                'is_active' => true,
                'slug' => 'temaki-saumon-avocat-concombre',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon avocat concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuca avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.90,
                'code' => 'F2',
                'is_active' => true,
                'slug' => 'temaki-thon-avocat-concombre',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Tempura crevette avocat concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Shrimp tempura avocado cucumber',
                        ],
                    ],
                ],
                'price' => 4.80,
                'code' => 'F3',
                'is_active' => true,
                'slug' => 'temaki-tempura-crevette-avocat-concombre',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Oeufs de saumon avocat concombre',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon eggs avocado cucumber',
                        ],
                    ],
                ],
                'price' => 6.00,
                'code' => 'F4',
                'is_active' => true,
                'slug' => 'temaki-oeufs-de-saumon-avocat-concombre',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'temaki-saumon-avocat-concombre')->exists()) {
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
