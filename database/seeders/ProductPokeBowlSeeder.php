<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductPokeBowlSeeder extends Seeder
{
    public function run()
    {
        $productTag = ProductTagTranslation::query()
            ->where('locale', 'FR')
            ->where('name', 'Poke bowl')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Salmon',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.80,
                'code' => 'R5',
                'is_active' => true,
                'slug' => 'pokebowl-saumon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Thon',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Tuna',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 15.80,
                'code' => 'R6',
                'is_active' => true,
                'slug' => 'pokebowl-thon',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Poulet',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.30,
                'code' => 'R7',
                'is_active' => true,
                'slug' => 'pokebowl-poulet',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Gyoza poulet frit  ',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Gyoza fried chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 13.30,
                'code' => 'R8',
                'is_active' => true,
                'slug' => 'pokebowl-gyoza-poulet-frit',
                'productTags' => [
                    'connect' => [$productTag],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Végétarien tofu',
                            'description' => 'Avocat, mangue, edamame, wakame, radis, sésame, oignons frits, ciboulette, choux',
                        ],
                        [
                            'locale' => 'EN',
                            'name' => 'Gyoza fried chicken',
                            'description' => 'Avocado, mango, edamame, wakame, radish, sesame, fried onions, chives, cabbage',
                        ],
                    ],
                ],
                'price' => 11.80,
                'code' => 'R9',
                'is_active' => true,
                'slug' => 'pokebowl-vegetarien-tofu',
                'productTags' => [
                    'connect' => [$productTag],
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
                $productItem->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
