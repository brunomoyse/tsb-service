<?php

namespace Database\Seeders;

use App\Models\Product;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class ProductSushiSeeder extends Seeder
{
    public function run()
    {
        $productTagSushi = ProductTagTranslation::query()
            ->where('language', 'FR')
            ->where('name', 'Sushi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A1',
                'is_active' => true,
                'slug' => 'sushi-saumon',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Crevette',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Shrimp',
                        ],
                    ],
                ],
                'price' => 1.90,
                'code' => 'A2',
                'is_active' => true,
                'slug' => 'sushi-crevette',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Thon',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Tuna',
                        ],
                    ],
                ],
                'price' => 2.20,
                'code' => 'A3',
                'is_active' => true,
                'slug' => 'sushi-thon',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Dorade',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Sea bream',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A4',
                'is_active' => true,
                'slug' => 'sushi-dorade',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Omelette',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Omelette',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A5',
                'is_active' => true,
                'slug' => 'sushi-omelette',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Octopus',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Octopus',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A6',
                'is_active' => true,
                'slug' => 'sushi-octopus',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Dorade grillée',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Grilled sea bream',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A7',
                'is_active' => true,
                'slug' => 'sushi-dorade-grillee',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Maquereau',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Mackerel',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A8',
                'is_active' => true,
                'slug' => 'sushi-surimi',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Surimi',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Surimi',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A9',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Avocat',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Avodaco',
                        ],
                    ],
                ],
                'price' => 1.80,
                'code' => 'A10',
                'is_active' => true,
                'slug' => 'sushi-avocat',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Anguille',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Eel',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A11',
                'is_active' => true,
                'slug' => 'sushi-anguille',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A12',
                'is_active' => true,
                'slug' => 'sushi-saumon-mi-cuit',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Thon mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked tuna',
                        ],
                    ],
                ],
                'price' => 2.60,
                'code' => 'A13',
                'is_active' => true,
                'slug' => 'sushi-thon-mi-cuit',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saint-Jacques mi-cuit',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Semi-cooked Saint-Jacques',
                        ],
                    ],
                ],
                'price' => 3.60,
                'code' => 'A14',
                'is_active' => true,
                'slug' => 'sushi-saint-jacques-mi-cuit',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Tofu',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Tofu',
                        ],
                    ],
                ],
                'price' => 2.00,
                'code' => 'A15',
                'is_active' => true,
                'slug' => 'sushi-tofu',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon cheese',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon cheese',
                        ],
                    ],
                ],
                'price' => 2.30,
                'code' => 'A16',
                'is_active' => true,
                'slug' => 'sushi-saumon-cheese',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Foie gras',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Foie gras',
                        ],
                    ],
                ],
                'price' => 3.50,
                'code' => 'A17',
                'is_active' => true,
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Maquereau grillé',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Grilled mackerel',
                        ],
                    ],
                ],
                'price' => 2.80,
                'code' => 'A18',
                'is_active' => true,
                'slug' => 'sushi-maquereau-grille',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon cuit caramélisé',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Caramelized cooked salmon',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A19',
                'is_active' => true,
                'slug' => 'sushi-saumon-cuit-caramelise',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'language' => 'FR',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'language' => 'EN',
                            'name' => 'Salmon avocado',
                        ],
                    ],
                ],
                'price' => 2.50,
                'code' => 'A20',
                'is_active' => true,
                'slug' => 'sushi-saumon-avocat',
                'productTags' => [
                    'connect' => [$productTagSushi],
                ],
            ],

        ];

        foreach ($products as $product) {
            try {
                /* @var Product $product */
                $product = Product::query()->create([
                    'price' => $product['price'],
                    'is_active' => true,
                    'slug' => $product['slug'],
                    'code' => $product['code'] ?? null,
                ]);

                $product->productTranslations()->createMany($product['productTranslations']['create']);
                $product->productTags()->sync($product['productTags']['connect']);
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
