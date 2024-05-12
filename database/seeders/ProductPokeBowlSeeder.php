<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductCategoryTranslation;
use Illuminate\Database\Seeder;

class ProductPokeBowlSeeder extends Seeder
{
    public function run()
    {
        $productCategory = ProductCategoryTranslation::query()
            ->where('locale', 'fr')
            ->where('name', 'Poke bowl')
            ->firstOrFail()->product_category_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Saumon',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Salmon',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.80,
                'code' => 'R5',
                'is_active' => true,
                'slug' => 'pokebowl-saumon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Thon',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Tuna',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 15.80,
                'code' => 'R6',
                'is_active' => true,
                'slug' => 'pokebowl-thon',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Poulet',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.30,
                'code' => 'R7',
                'is_active' => true,
                'slug' => 'pokebowl-poulet',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Gyoza poulet frit',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Gyoza fried chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.30,
                'code' => 'R8',
                'is_active' => true,
                'slug' => 'pokebowl-gyoza-poulet-frit',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'fr',
                            'name' => 'Végétarien tofu',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'en',
                            'name' => 'Gyoza fried chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 11.80,
                'code' => 'R9',
                'is_active' => true,
                'slug' => 'pokebowl-vegetarien-tofu',
                'productCategories' => [
                    'connect' => [$productCategory],
                ],
            ],
        ];

        if (Product::query()->where('slug', 'pokebowl-saumon')->exists()) {
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
