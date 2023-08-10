<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    /**
     * Run the migrations.
     */
    public function up(): void
    {
        Schema::create('product_product_tag', function (Blueprint $table) {
            $table->uuid('product_id');
            $table->foreign('product_id')->references('id')->on('products')->onDelete('cascade');
            $table->uuid('product_tag_id');
            $table->foreign('product_tag_id')->references('id')->on('product_tags')->onDelete('cascade');
            $table->unique(['product_id', 'product_tag_id']);
        });
    }

    /**
     * Reverse the migrations.
     */
    public function down(): void
    {
        Schema::dropIfExists('product_product_tag');
    }
};
