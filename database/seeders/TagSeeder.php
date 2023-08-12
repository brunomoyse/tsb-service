<?php

namespace Database\Seeders;

use App\Models\ProductTag;
use App\Models\ProductTagTranslation;
use Illuminate\Database\Seeder;

class TagSeeder extends Seeder
{
    public function run(): void
    {
        $tags = [
            [
                'EN' => 'Platter menu',
                'FR' => 'Menu plateau',

            ],
            [
                'EN' => 'Bento box menu',
                'FR' => 'Menu bento box',
            ],
            [
                'EN' => 'Sushi',
                'FR' => 'Sushi',
            ],
            [
                'EN' => 'Maki',
                'FR' => 'Maki',
            ],
            [
                'EN' => 'Gukan and tulip',
                'FR' => 'Gukans et tulipes',
            ],
            [
                'EN' => 'Spring roll',
                'FR' => 'Spring roll',
            ],
            [
                'EN' => 'California roll',
                'FR' => 'California roll',
            ],
            [
                'EN' => 'Temaki',
                'FR' => 'Temaki',
            ],
            [
                'EN' => 'Masago roll',
                'FR' => 'Masago roll',
            ],
            [
                'EN' => 'Special roll',
                'FR' => 'Spécial roll',
            ],
            [
                'EN' => 'Chirashi',
                'FR' => 'Chirashi',
            ],
            [
                'EN' => 'Sashimi',
                'FR' => 'Sashimi',
            ],
            [
                'EN' => 'Poke bowl',
                'FR' => 'Poke bowl',
            ],
            [
                'EN' => 'Tokyo hot',
                'FR' => 'Tokyo hot',
            ],
            [
                'EN' => 'Teppanyaki',
                'FR' => 'Teppanyaki',
            ],
            [
                'EN' => 'Side dish',
                'FR' => 'Accompagnement',
            ],
            [
                'EN' => 'Drink',
                'FR' => 'Boisson',
            ],
        ];

        foreach ($tags as $translations) {
            $exists = false;
            foreach ($translations as $language => $translation) {
                // Check if a tag with the specific translation already exists
                if (ProductTagTranslation::query()->where([
                    ['language', '=', $language],
                    ['name', '=', $translation],
                ])->exists()) {
                    $exists = true;
                    break;  // Break the inner loop if any translation exists
                }
            }

            if (! $exists) {
                $productTag = ProductTag::query()->create();
                $transData = [];
                foreach ($translations as $language => $translation) {
                    $transData[] = [
                        'language' => $language,
                        'name' => $translation,
                    ];
                }
                $productTag->productTagTranslations()->createMany($transData);
            }
        }
    }
}
