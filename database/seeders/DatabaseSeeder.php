<?php

namespace Database\Seeders;

// use Illuminate\Database\Console\Seeds\WithoutModelEvents;
use Illuminate\Database\Seeder;

class DatabaseSeeder extends Seeder
{
    /**
     * Seed the application's database.
     */
    public function run(): void
    {
        $this->call(TagSeeder::class);
        $this->call(ProductMenuPlateauSeeder::class);
        $this->call(ProductSushiSeeder::class);
        $this->call(ProductMenuBentoSeeder::class);
        $this->call(ProductMakiSeeder::class);
    }
}
