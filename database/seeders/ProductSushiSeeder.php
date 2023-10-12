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
            ->where('locale', 'FR')
            ->where('name', 'Sushi')
            ->firstOrFail()->product_tag_id;

        $products = [
            [
                'productTranslations' => [
                    'create' => [
                        [
                            'locale' => 'FR',
                            'name' => 'Saumon',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Crevette',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Thon',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Dorade',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Omelette',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Octopus',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Dorade grillée',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Maquereau',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Surimi',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Avocat',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Anguille',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Saumon mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Thon mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Saint-Jacques mi-cuit',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Tofu',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Saumon cheese',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Foie gras',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Maquereau grillé',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Saumon cuit caramélisé',
                        ],
                        [
                            'locale' => 'EN',
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
                            'locale' => 'FR',
                            'name' => 'Saumon avocat',
                        ],
                        [
                            'locale' => 'EN',
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

                if (isset($product['productTranslations']['create'])) {
                    $product->productTranslations()->createMany($product['productTranslations']['create']);
                    $product->productTags()->sync($product['productTags']['connect']);
                }
            } catch (\Exception $e) {
                throw new \Exception('Error creating product: '.$e->getMessage());
            }
        }
    }
}
